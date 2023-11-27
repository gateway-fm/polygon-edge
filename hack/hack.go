package main

import (
	"flag"
	"fmt"
	"math/big"

	bolt "go.etcd.io/bbolt"
)

var path = ""
var command = ""

func main() {
	flag.StringVar(&path, "path", "", "Path to the file")
	flag.StringVar(&command, "command", "", "Command to run")
	flag.Parse()

	switch command {
	case "validator-snapshot":
		validatorSnapshot()
	default:
		fmt.Print("Unknown command")
	}
}

func validatorSnapshot() {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		fmt.Print(err)
		return
	}
	defer db.Close()

	bucket := []byte("validatorSnapshots")

	err = db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucket).Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			key := new(big.Int).SetBytes(k)
			fmt.Printf("key=%v, value=%s\n", key.Uint64(), v)
			fmt.Println()
		}

		return nil
	})

	if err != nil {
		fmt.Print(err)
	}
}
