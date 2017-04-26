package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/NebulousLabs/Sia/api"
)

func main() {
	if len(os.Args) == 1 {
		fmt.Println("usage: siasync [folder]")
		os.Exit(1)
	}
	sf, err := NewSiafolder(os.Args[1], api.NewClient("localhost:9980", ""))
	if err != nil {
		log.Fatal(err)
	}
	defer sf.Close()

	log.Println("watching for changes to ", os.Args[1])

	done := make(chan os.Signal)
	signal.Notify(done, os.Interrupt)
	<-done
	fmt.Println("caught quit signal, exiting...")
}
