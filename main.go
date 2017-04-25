package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/fsnotify/fsnotify"

	"github.com/NebulousLabs/Sia/api"
)

// SiaFolder is a folder that is synchronized to a Sia node.
type SiaFolder struct {
	path    string
	client  *api.Client
	watcher *fsnotify.Watcher

	files map[string]string // files is a map of file paths to SHA256 checksums, used to reconcile file changes
}

// NewSiafolder creates a new SiaFolder using the provided path and api
// address.
func NewSiafolder(path string, apiaddr string) (*SiaFolder, error) {
	sf := &SiaFolder{}
	sf.path = path
	sf.files = make(map[string]string)

	sf.client = api.NewClient(apiaddr, "")
	var contracts api.RenterContracts
	err := sf.client.Get("/renter/contracts", &contracts)
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
	err = watcher.Add(path)
	if err != nil {
		return nil, err
	}

	sf.watcher = watcher

	// walk the provided path, accumulating a slice of files to potentially
	// upload and adding any subdirectories to the watcher.
	var files []string
	err = filepath.Walk(path, func(walkpath string, f os.FileInfo, err error) error {
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
			relpath, err := filepath.Rel(path, walkpath)
			if err != nil {
				return err
			}
			files = append(files, relpath)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// upload any files not in the sia node already
	err = sf.uploadNonExisting(files)
	if err != nil {
		return nil, err
	}

	// perform initial hashing to track file changes
	for _, file := range files {
		checksum, err := checksumFile(file)
		if err != nil {
			return nil, err
		}
		sf.files[file] = checksum
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
		case event := <-sf.watcher.Events:
			f, err := os.Stat(event.Name)
			if err != nil {
				log.Println("error opening updated file:", err)
				continue
			}
			if f.IsDir() {
				sf.watcher.Add(event.Name)
				continue
			}

			// WRITE event, checksum the file and re-upload it if it has changed
			if event.Op&fsnotify.Write == fsnotify.Write {
				checksum, err := checksumFile(event.Name)
				if err != nil {
					log.Println("error getting checksum:", err)
					continue
				}
				oldChecksum, exists := sf.files[event.Name]
				if exists && oldChecksum != checksum {
					log.Printf("change in %v detected, re-uploading\n", event.Name)
					sf.handleRemove(event.Name)
					sf.handleCreate(event.Name)
				}
				sf.files[event.Name] = checksum
			}

			// REMOVE event
			if event.Op&fsnotify.Remove == fsnotify.Remove {
				log.Println("sync updated detected, removing", event.Name)
				sf.handleRemove(event.Name)
			}

			// CREATE event
			if event.Op&fsnotify.Create == fsnotify.Create {
				log.Println("sync updated detected, uploading", event.Name)
				sf.handleCreate(event.Name)
			}

		case err := <-sf.watcher.Errors:
			log.Println("fsevents error:", err)
		}
	}
}

// Close releases any resources allocated by a SiaFolder.
func (sf *SiaFolder) Close() error {
	return sf.watcher.Close()
}

// handleCreate handles a file creation event. `file` is a relative path to the
// file on disk.
func (sf *SiaFolder) handleCreate(file string) {
	abspath, err := filepath.Abs(file)
	if err != nil {
		log.Println("error getting absolute path to upload:", err)
		return
	}
	relpath, err := filepath.Rel(sf.path, file)
	if err != nil {
		log.Println("error getting relative path to upload:", err)
		return
	}
	err = sf.client.Post(fmt.Sprintf("/renter/upload/%v", relpath), fmt.Sprintf("source=%v", abspath), nil)
	if err != nil {
		log.Printf("error uploading %v: %v\n", file, err)
	}
}

// handleRemove handles a file removal event.
func (sf *SiaFolder) handleRemove(file string) {
	relpath, err := filepath.Rel(sf.path, file)
	if err != nil {
		log.Println("error getting relative path to remove:", err)
		return
	}
	err = sf.client.Post(fmt.Sprintf("/renter/delete/%v", relpath), "", nil)
	if err != nil {
		log.Printf("error removing %v: %v\n", file, err)
	}
}

// uploadNonExisting runs once and performs any uploads required to ensure
// every file in files is uploaded to the Sia node.
func (sf *SiaFolder) uploadNonExisting(files []string) error {
	var renterFiles api.RenterFiles
	err := sf.client.Get("/renter/files", &renterFiles)
	if err != nil {
		return err
	}

	for _, diskfile := range files {
		exists := false
		for _, siafile := range renterFiles.Files {
			if siafile.SiaPath == diskfile {
				exists = true
			}
		}

		if !exists {
			sf.handleCreate(diskfile)
		}
	}

	return nil
}

func main() {
	if len(os.Args) == 1 {
		fmt.Println("usage: siasync [folder]")
		os.Exit(1)
	}
	sf, err := NewSiafolder(os.Args[1], "localhost:9980")
	if err != nil {
		log.Fatal(err)
	}
	defer sf.Close()

	done := make(chan os.Signal)
	signal.Notify(done, os.Interrupt)
	<-done
}
