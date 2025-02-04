// This code is available on the terms of the project LICENSE.md file,
// also available online at https://blueoakcouncil.org/license/1.0.0.

package bolt

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"decred.org/dcrdex/dex/order"

	dexdb "decred.org/dcrdex/client/db"
	"go.etcd.io/bbolt"
)

var dbUpgradeTests = [...]struct {
	name       string
	upgrade    upgradefunc
	verify     func(*testing.T, *bbolt.DB)
	filename   string // in testdata directory
	newVersion uint32
}{
	// {"testnetbot", v4Upgrade, verifyV4Upgrade, "dexbot-testnet.db.gz", 4}, // only for TestUpgradeDB
	{"upgradeFromV0", v1Upgrade, verifyV1Upgrade, "v0.db.gz", 1},
	{"upgradeFromV1", v2Upgrade, verifyV2Upgrade, "v1.db.gz", 2},
	{"upgradeFromV2", v3Upgrade, verifyV3Upgrade, "v2.db.gz", 3},
	{"upgradeFromV3", v4Upgrade, verifyV4Upgrade, "v3.db.gz", 4},
	{"upgradeFromV4", v5Upgrade, verifyV5Upgrade, "v4.db.gz", 5},
}

func TestUpgrades(t *testing.T) {
	d, err := os.MkdirTemp("", "dcrdex_test_upgrades")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("group", func(t *testing.T) {
		for _, tc := range dbUpgradeTests {
			tc := tc // capture range variable
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				testFile, err := os.Open(filepath.Join("testdata", tc.filename))
				if err != nil {
					t.Fatal(err)
				}
				defer testFile.Close()
				r, err := gzip.NewReader(testFile)
				if err != nil {
					t.Fatal(err)
				}
				dbPath := filepath.Join(d, tc.name+".db")
				fi, err := os.Create(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.Copy(fi, r)
				fi.Close()
				if err != nil {
					t.Fatal(err)
				}
				db, err := bbolt.Open(dbPath, 0600,
					&bbolt.Options{Timeout: 1 * time.Second})
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				err = db.Update(func(dbtx *bbolt.Tx) error {
					return doUpgrade(dbtx, tc.upgrade, tc.newVersion)
				})
				if err != nil {
					t.Fatalf("Upgrade %d -> %d failed: %v", tc.newVersion-1, tc.newVersion, err)
				}
				tc.verify(t, db)
			})
		}
	})

	os.RemoveAll(d)
}

func TestUpgradeDB(t *testing.T) {
	runUpgrade := func(archiveName string) error {
		dbPath, cleanup := unpack(t, archiveName)
		defer cleanup()
		// NewDB runs upgradeDB.
		dbi, err := NewDB(dbPath, tLogger)
		if err != nil {
			return fmt.Errorf("database initialization or upgrade error: %w", err)
		}
		db := dbi.(*BoltDB)
		// Run upgradeDB again and it should be happy.
		err = db.upgradeDB()
		if err != nil {
			return fmt.Errorf("upgradeDB error: %v", err)
		}
		newVersion, err := db.getVersion()
		if err != nil {
			return fmt.Errorf("getVersion error: %v", err)
		}
		if newVersion != DBVersion {
			return fmt.Errorf("DB version not set. Expected %d, got %d", DBVersion, newVersion)
		}
		return nil
	}

	for _, tt := range dbUpgradeTests {
		err := runUpgrade(tt.filename)
		if err != nil {
			t.Fatalf("upgrade error for version %d database: %v", tt.newVersion-1, err)
		}
	}

}

func verifyV1Upgrade(t *testing.T, db *bbolt.DB) {
	t.Helper()
	err := db.View(func(dbtx *bbolt.Tx) error {
		return checkVersion(dbtx, 1)
	})
	if err != nil {
		t.Error(err)
	}
}

func verifyV2Upgrade(t *testing.T, db *bbolt.DB) {
	t.Helper()
	maxFeeB := uint64Bytes(^uint64(0))
	ordersBucket := []byte("orders")

	err := db.View(func(dbtx *bbolt.Tx) error {
		err := checkVersion(dbtx, 2)
		if err != nil {
			return err
		}

		master := dbtx.Bucket(ordersBucket)
		if master == nil {
			return fmt.Errorf("orders bucket not found")
		}
		return master.ForEach(func(oid, _ []byte) error {
			oBkt := master.Bucket(oid)
			if oBkt == nil {
				return fmt.Errorf("order %x bucket is not a bucket", oid)
			}
			if !bytes.Equal(oBkt.Get(maxFeeRateKey), maxFeeB) {
				return fmt.Errorf("max fee not upgraded")
			}
			return nil
		})
	})
	if err != nil {
		t.Error(err)
	}
}

// Nothing to really check here. Any errors would have come out during the
// upgrade process itself, since we just added a default nil field.
func verifyV3Upgrade(t *testing.T, db *bbolt.DB) {
	t.Helper()
	err := db.View(func(dbtx *bbolt.Tx) error {
		return checkVersion(dbtx, 3)
	})
	if err != nil {
		t.Error(err)
	}
}

func verifyV4Upgrade(t *testing.T, db *bbolt.DB) {
	oldOrdersBucket := []byte("orders")
	newActiveOrdersBucket := []byte("activeOrders")
	err := db.View(func(dbtx *bbolt.Tx) error {
		err := checkVersion(dbtx, 4)
		if err != nil {
			return err
		}
		// Ensure we have both old and new buckets.
		archivedOrdersBkt := dbtx.Bucket(oldOrdersBucket)
		if archivedOrdersBkt == nil {
			return fmt.Errorf("archived orders bucket not found")
		}
		activeOrdersBkt := dbtx.Bucket(newActiveOrdersBucket)
		if activeOrdersBkt == nil {
			return fmt.Errorf("active orders bucket not found")
		}

		// Ensure the old bucket now only contains finished orders.
		err = archivedOrdersBkt.ForEach(func(k, _ []byte) error {
			archivedOBkt := archivedOrdersBkt.Bucket(k)
			if archivedOBkt == nil {
				return fmt.Errorf("order %x bucket is not a bucket", k)
			}
			status := order.OrderStatus(intCoder.Uint16(archivedOBkt.Get(statusKey)))
			if status == order.OrderStatusUnknown {
				fmt.Printf("Encountered order with unknown status: %x\n", k)
				return nil
			}
			if status.IsActive() {
				return fmt.Errorf("archived bucket has active order: %x", k)
			}
			return nil
		})
		if err != nil {
			return err
		}

		// Ensure the new bucket only contains active orders.
		err = activeOrdersBkt.ForEach(func(k, _ []byte) error {
			activeOBkt := activeOrdersBkt.Bucket(k)
			if activeOBkt == nil {
				return fmt.Errorf("order %x bucket is not a bucket", k)
			}
			status := order.OrderStatus(intCoder.Uint16(activeOBkt.Get(statusKey)))
			if status == order.OrderStatusUnknown {
				return fmt.Errorf("encountered order with unknown status: %x", k)
			}
			if !status.IsActive() {
				return fmt.Errorf("active orders bucket has archived order: %x", k)
			}
			return nil
		})
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

// Ensure that the LegacyEncKey field is populated for the accounts in the DB.
func verifyV5Upgrade(t *testing.T, db *bbolt.DB) {
	if err := db.View(func(tx *bbolt.Tx) error {
		return checkVersion(tx, 5)
	}); err != nil {
		t.Error(err)
	}

	if err := db.View(func(tx *bbolt.Tx) error {
		accts := tx.Bucket(accountsBucket)
		c := accts.Cursor()
		for acctKey, _ := c.First(); acctKey != nil; acctKey, _ = c.Next() {
			acct := accts.Bucket(acctKey)
			if acct == nil {
				return fmt.Errorf("account bucket %s value not a nested bucket", string(acctKey))
			}
			acctB := getCopy(acct, accountKey)
			if acctB == nil {
				return fmt.Errorf("empty account found for %s", string(acctKey))
			}
			acctInfo, err := dexdb.DecodeAccountInfo(acctB)
			if err != nil {
				return err
			}
			if len(acctInfo.LegacyEncKey) == 0 {
				return errors.New("LegacyEncKey not sets")
			}
		}
		return nil
	}); err != nil {
		t.Error(err)
	}
}

func checkVersion(dbtx *bbolt.Tx, expectedVersion uint32) error {
	bkt := dbtx.Bucket(appBucket)
	if bkt == nil {
		return fmt.Errorf("appBucket not found")
	}
	versionB := bkt.Get(versionKey)
	if versionB == nil {
		return fmt.Errorf("expected a non-nil version value")
	}
	version := intCoder.Uint32(versionB)
	if version != expectedVersion {
		return fmt.Errorf("expected db version %d, got %d",
			expectedVersion, version)
	}
	return nil
}

func unpack(t *testing.T, db string) (string, func()) {
	t.Helper()
	d, err := os.MkdirTemp("", "dcrdex_test_upgrades")
	if err != nil {
		t.Fatal(err)
	}

	t.Helper()
	archive, err := os.Open(filepath.Join("testdata", db))
	if err != nil {
		t.Fatal(err)
	}

	r, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(d, strings.TrimSuffix(db, ".gz"))
	dbFile, err := os.Create(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(dbFile, r)
	archive.Close()
	dbFile.Close()
	if err != nil {
		os.RemoveAll(d)
		t.Fatal(err)
	}
	return dbPath, func() {
		os.RemoveAll(d)
	}
}
