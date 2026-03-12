package lora

import (
	"context"
	"os"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	zgcommon "github.com/0gfoundation/0g-storage-client/common"
	"github.com/0gfoundation/0g-storage-client/indexer"
	"github.com/sirupsen/logrus"
)

// StorageDownloader handles downloading encrypted adapter files from 0G Storage
// and decrypting them using the provider wallet's ECIES/AES keys.
type StorageDownloader struct {
	indexerClient *indexer.Client
	providerKey   string
	logger        log.Logger
}

func NewStorageDownloader(cfg config.LoRAConfig, providerKey string, logger log.Logger) (*StorageDownloader, error) {
	if cfg.StorageIndexerUrl == "" {
		return nil, errors.New("storageIndexerUrl not configured")
	}

	indexerClient, err := indexer.NewClient(cfg.StorageIndexerUrl, indexer.IndexerClientOption{
		LogOption: zgcommon.LogOption{LogLevel: logrus.InfoLevel},
	})
	if err != nil {
		return nil, errors.Wrap(err, "create indexer client")
	}

	return &StorageDownloader{
		indexerClient: indexerClient,
		providerKey:   providerKey,
		logger:        logger,
	}, nil
}

// DownloadAndDecrypt downloads the encrypted adapter from 0G Storage,
// decrypts the AES key using the provider's ECIES private key,
// then decrypts the adapter file with AES-GCM, and unzips it.
//
// storageHashHex:   hex-encoded 0G Storage root hash (no 0x prefix)
// providerEncKey:   provider-ECIES-encrypted AES key bytes
// outputDir:        directory where the decrypted adapter will be unzipped
func (d *StorageDownloader) DownloadAndDecrypt(ctx context.Context, storageHashHex string, providerEncKey []byte, outputDir string) error {
	// Step 1: Decrypt AES key
	d.logger.Infof("decrypting AES key with provider ECIES private key (%d encrypted bytes)", len(providerEncKey))
	aesKey, err := util.ProviderECIESDecrypt(d.providerKey, providerEncKey)
	if err != nil {
		return errors.Wrap(err, "ECIES decrypt AES key")
	}
	d.logger.Infof("AES key decrypted successfully (%d bytes)", len(aesKey))

	// Step 2: Download encrypted file from 0G Storage
	encryptedFile := outputDir + "_encrypted.download"
	defer func() {
		_ = os.Remove(encryptedFile)
	}()

	rootWithPrefix := "0x" + storageHashHex
	d.logger.Infof("downloading encrypted adapter from 0G Storage (hash: %s)", rootWithPrefix)
	if err := d.indexerClient.Download(ctx, rootWithPrefix, encryptedFile, true); err != nil {
		return errors.Wrapf(err, "download from 0G Storage (hash: %s)", rootWithPrefix)
	}

	fi, _ := os.Stat(encryptedFile)
	d.logger.Infof("downloaded %d bytes from 0G Storage", fi.Size())

	// Step 3: Decrypt with AES-GCM
	decryptedZip := outputDir + "_decrypted.zip"
	defer func() {
		_ = os.Remove(decryptedZip)
	}()

	d.logger.Infof("decrypting adapter with AES-GCM")
	if err := util.AesDecryptLargeFile(aesKey, encryptedFile, decryptedZip); err != nil {
		return errors.Wrap(err, "AES decrypt adapter file")
	}

	// Step 4: Unzip to output directory
	d.logger.Infof("unzipping adapter to %s", outputDir)
	unzippedDir, err := util.Unzip(decryptedZip, outputDir)
	if err != nil {
		return errors.Wrap(err, "unzip adapter")
	}

	d.logger.Infof("adapter extracted to %s", unzippedDir)
	return nil
}
