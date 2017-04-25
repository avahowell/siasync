package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/fsnotify/fsnotify"

	"github.com/NebulousLabs/Sia/api"
)

type SiaFolder struct {
	client  *api.Client
	watcher *fsnotify.Watcher
}

func (sf *SiaFolder) handleCreate(file string) {
	log.Println("sync updated detected, uploading", file)
	abspath, err := filepath.Abs(file)
	if err != nil {
		log.Println("error getting absolute path to upload:", err)
		return
	}
	err = sf.client.Post(fmt.Sprintf("/renter/upload/%v", file), fmt.Sprintf("source=%v", abspath), nil)
	if err != nil {
		log.Printf("error uploading %v: %v\n", file, err)
	}
}

func (sf *SiaFolder) handleRemove(file string) {
	log.Println("sync updated detected, removing", file)
	err := sf.client.Post(fmt.Sprintf("/renter/delete/%v", file), "", nil)
	if err != nil {
		log.Printf("error removing %v: %v\n", file, err)
	}
}

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

func OpenSiafolder(path string, apiaddr string) (*SiaFolder, error) {
	sf := &SiaFolder{}

	sf.client = api.NewClient(apiaddr, "")
	var contracts api.RenterContracts
	err := sf.client.Get("/renter/contracts", &contracts)
	if err != nil {
		return nil, err
	}
	if len(contracts.Contracts) == 0 {
		return nil, errors.New("you must have formed contracts to upload to Sia")
	}

	// walk the provided path
	var files []string
	err = filepath.Walk(path, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		files = append(files, path)
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

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	go func() {
		for {
			select {
			case event := <-watcher.Events:
				if event.Op&fsnotify.Remove == fsnotify.Remove {
					sf.handleRemove(event.Name)
				}

				if event.Op&fsnotify.Create == fsnotify.Create {
					sf.handleCreate(event.Name)
				}

				if event.Op&fsnotify.Rename == fsnotify.Rename {
					sf.handleRemove(event.Name)
				}
			case err := <-watcher.Errors:
				log.Println("error:", err)
			}
		}
	}()

	err = watcher.Add(path)
	if err != nil {
		return nil, err
	}

	sf.watcher = watcher
	return sf, nil
}

func (sf *SiaFolder) Close() error {
	return sf.watcher.Close()
}

func main() {
	if len(os.Args) == 1 {
		fmt.Println("usage: siasync [folder]")
		os.Exit(1)
	}
	sf, err := OpenSiafolder(os.Args[1], "localhost:9980")
	if err != nil {
		log.Fatal(err)
	}
	defer sf.Close()

	done := make(chan os.Signal)
	signal.Notify(done, os.Interrupt)
	<-done
}
