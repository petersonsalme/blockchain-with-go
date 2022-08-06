package main

import (
	"log"

	"github.com/joho/godotenv"
	"github.com/petersonsalme/go-blockchain/server"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

	log.Fatal(server.Run())
}
