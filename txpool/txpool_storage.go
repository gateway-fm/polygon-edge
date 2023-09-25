package txpool

import (
	bolt "go.etcd.io/bbolt"
)

var bucket = []byte("txpool")

type StorageImpl struct {
	db *bolt.DB
}

func NewStorage(db *bolt.DB) *StorageImpl {
	return &StorageImpl{
		db: db,
	}
}

func (t *StorageImpl) Close() error {
	return t.db.Close()
}

func (t *StorageImpl) Setup() error {
	return t.db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
			return err
		}

		return nil
	})
}

func (t *StorageImpl) Get(key []byte) ([]byte, error) {
	var value []byte

	err := t.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		value = b.Get(key)

		return nil
	})

	return value, err
}

func (t *StorageImpl) Put(key []byte, value []byte) error {
	return t.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		return b.Put(key, value)
	})
}
