package main

import (
	"log"

	"qi/internal/commands"
)

func main() {
	if err := commands.NewRootCommand().Execute(); err != nil {
		log.Fatal(err)
	}
}
