package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NebulousLabs/Sia/api"
)

var testFiles = []string{"test", "testdir/testfile3.txt", "testdir/testdir2/testfile4.txt", "testfile1.txt", "testfile2.txt"}

const testDir = "test"

type testingClient struct {
	siaFiles map[string]string
}

func newTestingClient() *testingClient {
	return &testingClient{
		siaFiles: make(map[string]string),
	}
}

func (t *testingClient) Get(resource string, obj interface{}) error {
	if resource == "/renter/contracts" {
		rc := obj.(*api.RenterContracts)
		rc.Contracts = make([]api.RenterContract, 50)
		return nil
	}
	return nil
}

func (t *testingClient) Post(resource string, data string, obj interface{}) error {
	params, err := url.ParseQuery(data)
	if err != nil {
		return err
	}
	if strings.Index(resource, "/renter/upload/") == 0 {
		path := strings.Split(resource, "/renter/upload/")[1]
		checksum, err := checksumFile(params.Get("source"))
		if err != nil {
			return err
		}
		t.siaFiles[path] = checksum
	}
	if strings.Index(resource, "/renter/delete/") == 0 {
		path := strings.Split(resource, "/renter/delete/")[1]
		delete(t.siaFiles, path)
	}
	return nil
}

func TestSiafolder(t *testing.T) {
	mockClient := newTestingClient()

	sf, err := NewSiafolder(testDir, mockClient)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()

	// should have uploaded all of our test files
	for _, file := range testFiles {
		if _, exists := mockClient.siaFiles[file]; !exists {
			t.Fatal("our test files should have initially been uploaded if they didnt exist")
		}
	}
}

// TestSiafolderCreateDelete verifies that files created or removed in the
// watched directory are correctly uploaded and deleted.
func TestSiafolderCreateDelete(t *testing.T) {
	mockClient := newTestingClient()
	sf, err := NewSiafolder(testDir, mockClient)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()

	// create a new file and write some data to it and verify that it gets
	// uploaded
	newfile := filepath.Join(testDir, "newfile")
	f, err := os.Create(newfile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()

	// wait a bit for the filesystem event to propogate
	time.Sleep(time.Second)

	if _, exists := mockClient.siaFiles["newfile"]; !exists {
		t.Fatal("newfile should have been uploaded when it was created on disk")
	}

	// delete the file
	f.Close()
	os.Remove(f.Name())

	time.Sleep(time.Second)

	if _, exists := mockClient.siaFiles["newfile"]; exists {
		t.Fatal("newfile should have been deleted when it was removed on disk")
	}

	// test that events propogate in nested directories
	newfile = filepath.Join(testDir, "testdir/newfile")
	f, err = os.Create(newfile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()

	time.Sleep(time.Second)

	if _, exists := mockClient.siaFiles["testdir/newfile"]; !exists {
		t.Fatal("newfile should have been uploaded when it was created on disk")
	}

	// delete the file
	f.Close()
	os.Remove(f.Name())

	time.Sleep(time.Second)

	if _, exists := mockClient.siaFiles["testdir/newfile"]; exists {
		t.Fatal("newfile should have been deleted when it was removed on disk")
	}
}

// TestSiafolderCreateDirectory verifies that files in newly created
// directories under the watched directory get correctly uploaded.
func TestSiafolderCreateDirectory(t *testing.T) {
	mockClient := newTestingClient()
	sf, err := NewSiafolder(testDir, mockClient)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()

	testCreateDir := filepath.Join(testDir, "newdir")
	err = os.Mkdir(testCreateDir, 0755)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(testCreateDir)

	// should not upload empty directories
	time.Sleep(time.Second)

	if _, exists := mockClient.siaFiles["newdir"]; exists {
		t.Fatal("should not upload empty directories")
	}

	testFile := filepath.Join(testCreateDir, "testfile")
	f, err := os.Create(testFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	time.Sleep(time.Second)

	if _, exists := mockClient.siaFiles["newdir/testfile"]; !exists {
		t.Fatal("should have uploaded file in newly created directory")
	}
}

// TestSiafolderFileWrite verifies that a file is deleted and re-uploaded when
// it is changed on disk.
func TestSiafolderFileWrite(t *testing.T) {
	mockClient := newTestingClient()
	sf, err := NewSiafolder(testDir, mockClient)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()

	newfile := filepath.Join(testDir, "newfile")
	f, err := os.Create(newfile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		f.Close()
		os.Remove(f.Name())
	}()

	// wait a bit for the filesystem event to propogate
	time.Sleep(time.Second)

	oldChecksum, exists := mockClient.siaFiles["newfile"]
	if !exists {
		t.Fatal("newfile should have been uploaded when it was created on disk")
	}

	// write some data to the file and verify that it is updated
	_, err = f.Write([]byte{40, 40, 40, 40, 40})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Second)

	newChecksum, exists := mockClient.siaFiles["newfile"]
	if !exists {
		t.Fatal("newfile did not exist after writing data to it")
	}
	if newChecksum == oldChecksum {
		t.Fatal("checksum did not change")
	}
}
