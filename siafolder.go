package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"

	"github.com/NebulousLabs/Sia/api"
)

type apiClient interface {
	Get(string, interface{}) error
	Post(string, string, interface{}) error
}

// SiaFolder is a folder that is synchronized to a Sia node.
type SiaFolder struct {
	path    string
	client  apiClient
	watcher *fsnotify.Watcher

	files map[string]string // files is a map of file paths to SHA256 checksums, used to reconcile file changes

	closeChan chan struct{}
}

// NewSiafolder creates a new SiaFolder using the provided path and api
// address.
func NewSiafolder(path string, client apiClient) (*SiaFolder, error) {
	sf := &SiaFolder{}

	abspath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	sf.path = abspath
	sf.files = make(map[string]string)
	sf.closeChan = make(chan struct{})
	sf.client = client

	var contracts api.RenterContracts
	err = sf.client.Get("/renter/contracts", &contracts)
	if err != nil {
		return nil, err
	}
	if len(contracts.Contracts) == 0 {
		return nil, errors.New("you must have formed contracts to upload to Sia")
	}

	// watch for file changes
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	err = watcher.Add(abspath)
	if err != nil {
		return nil, err
	}

	sf.watcher = watcher

	// walk the provided path, accumulating a slice of files to potentially
	// upload and adding any subdirectories to the watcher.
	err = filepath.Walk(abspath, func(walkpath string, f os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if walkpath == path {
			return nil
		}

		if f.IsDir() {
			// subdirectories must be added to the watcher.
			watcher.Add(walkpath)
		} else {
			checksum, err := checksumFile(walkpath)
			if err != nil {
				return err
			}
			sf.files[walkpath] = checksum
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = sf.uploadNonExisting()
	if err != nil {
		return nil, err
	}

	go sf.eventWatcher()

	return sf, nil
}

// checksumFile returns a sha256 checksum of a given file on disk.
func checksumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return string(h.Sum(nil)), nil
}

// eventWatcher continuously listens on the SiaFolder's watcher channels and
// performs the necessary upload/delete operations.
func (sf *SiaFolder) eventWatcher() {
	for {
		select {
		case <-sf.closeChan:
			return
		case event := <-sf.watcher.Events:
			filename := filepath.Clean(event.Name)
			f, err := os.Stat(filename)
			if err == nil && f.IsDir() {
				sf.watcher.Add(filename)
				continue
			}

			// WRITE event, checksum the file and re-upload it if it has changed
			if event.Op&fsnotify.Write == fsnotify.Write {
				err = sf.handleFileWrite(filename)
				if err != nil {
					log.Println(err)
				}
			}

			// REMOVE event
			if event.Op&fsnotify.Remove == fsnotify.Remove {
				log.Println("file removal detected, removing", filename)
				err = sf.handleRemove(filename)
				if err != nil {
					log.Println(err)
				}
			}

			// CREATE event
			if event.Op&fsnotify.Create == fsnotify.Create {
				log.Println("file creation detected, uploading", filename)
				err = sf.handleCreate(filename)
				if err != nil {
					log.Println(err)
				}
			}

		case err := <-sf.watcher.Errors:
			if err != nil {
				log.Println("fsevents error:", err)
			}
		}
	}
}

// handleFileWrite handles a WRITE fsevent.
func (sf *SiaFolder) handleFileWrite(file string) error {
	checksum, err := checksumFile(file)
	if err != nil {
		return err
	}

	oldChecksum, exists := sf.files[file]
	if exists && oldChecksum != checksum {
		log.Printf("change in %v detected, reuploading..\n", file)
		sf.files[file] = checksum
		err = sf.handleRemove(file)
		if err != nil {
			return err
		}
		err = sf.handleCreate(file)
		if err != nil {
			return err
		}
	}

	return nil
}

// Close releases any resources allocated by a SiaFolder.
func (sf *SiaFolder) Close() error {
	close(sf.closeChan)
	return sf.watcher.Close()
}

// handleCreate handles a file creation event. `file` is a relative path to the
// file on disk.
func (sf *SiaFolder) handleCreate(file string) error {
	abspath, err := filepath.Abs(file)
	if err != nil {
		return fmt.Errorf("error getting absolute path to upload: %v\n:", err)
	}
	relpath, err := filepath.Rel(sf.path, file)
	if err != nil {
		return fmt.Errorf("error getting relative path to upload: %v\n", err)
	}
	err = sf.client.Post(fmt.Sprintf("/renter/upload/%v", relpath), fmt.Sprintf("source=%v", abspath), nil)
	if err != nil {
		return fmt.Errorf("error uploading %v: %v\n", file, err)
	}
	checksum, err := checksumFile(file)
	if err != nil {
		return err
	}
	sf.files[file] = checksum
	return nil
}

// handleRemove handles a file removal event.
func (sf *SiaFolder) handleRemove(file string) error {
	relpath, err := filepath.Rel(sf.path, file)
	if err != nil {
		return fmt.Errorf("error getting relative path to remove: %v\n", err)
	}
	err = sf.client.Post(fmt.Sprintf("/renter/delete/%v", relpath), "", nil)
	if err != nil {
		return fmt.Errorf("error removing %v: %v\n", file, err)
	}
	delete(sf.files, file)
	return nil
}

// uploadNonExisting runs once and performs any uploads required to ensure
// every file in files is uploaded to the Sia node.
func (sf *SiaFolder) uploadNonExisting() error {
	var renterFiles api.RenterFiles
	err := sf.client.Get("/renter/files", &renterFiles)
	if err != nil {
		return err
	}

	for file := range sf.files {
		relpath, err := filepath.Rel(sf.path, file)
		if err != nil {
			return err
		}
		exists := false
		for _, siafile := range renterFiles.Files {
			if siafile.SiaPath == relpath {
				exists = true
			}
		}

		if !exists {
			sf.handleCreate(file)
		}
	}

	return nil
}
