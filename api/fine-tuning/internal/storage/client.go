package storage

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"

	"github.com/0glabs/0g-serving-broker/common/chain"
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	"github.com/0gfoundation/0g-storage-client/common"
	"github.com/0gfoundation/0g-storage-client/common/blockchain"
	"github.com/0gfoundation/0g-storage-client/core"
	"github.com/0gfoundation/0g-storage-client/indexer"
	"github.com/0gfoundation/0g-storage-client/transfer"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/openweb3/web3go"
	"github.com/sirupsen/logrus"
)

var nRetriesToUpload = 10
var uploadMethod = "max"

type Client struct {
	w3Client              *web3go.Client
	storageUploadUrgs     *config.UploadArgs
	indexerStandardClient *indexer.Client
	indexerTurboClient    *indexer.Client
	logger                log.Logger
	MaxGasPrice           *big.Int
	NRetries              int
	Method                string
}

func New(config *config.Config, logger log.Logger) (*Client, error) {
	zgConfig, err := chain.NewEthereumNetwork(&config.Network)
	if err != nil {
		panic(err)
	}

	wallets, err := zgConfig.Wallets()
	if err != nil {
		panic(err)
	}
	wallet, err := wallets.Wallet(0)
	if err != nil {
		panic(err)
	}

	logger.WithFields(logrus.Fields{
		"wallet": wallet.Address(),
		"url":    zgConfig.URL(),
	}).Info("Wallet and URL")

	w3client := blockchain.MustNewWeb3(zgConfig.URL(), wallet.PrivateKey(), config.ProviderOption)
	if config.GasPrice != "" {
		gasPrice, err := strconv.ParseUint(config.GasPrice, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid gas price: %v", err)
		}
		blockchain.CustomGasPrice = gasPrice
	}

	indexerStandardClient, err := indexer.NewClient(config.StorageClientConfig.IndexerStandard, indexer.IndexerClientOption{
		ProviderOption: config.ProviderOption,
		LogOption:      common.LogOption{LogLevel: logrus.InfoLevel},
	})
	if err != nil {
		return nil, err
	}

	indexerTurboClient, err := indexer.NewClient(config.StorageClientConfig.IndexerTurbo, indexer.IndexerClientOption{
		ProviderOption: config.ProviderOption,
		LogOption:      common.LogOption{LogLevel: logrus.InfoLevel},
	})
	if err != nil {
		return nil, err
	}

	maxGasPrice, err := util.ConvertToBigInt(config.MaxGasPrice)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid max gas price: %v", config.MaxGasPrice)
	}

	return &Client{
		w3Client:              w3client,
		storageUploadUrgs:     &config.StorageClientConfig.UploadArgs,
		indexerStandardClient: indexerStandardClient,
		indexerTurboClient:    indexerTurboClient,
		logger:                logger,
		MaxGasPrice:           maxGasPrice,
		NRetries:              nRetriesToUpload,
		Method:                uploadMethod,
	}, nil
}

func (c *Client) DownloadFromStorage(ctx context.Context, hash, filePath string, isTurbo bool) (string, error) {
	var indexerClient *indexer.Client
	if isTurbo {
		indexerClient = c.indexerTurboClient
	} else {
		indexerClient = c.indexerStandardClient
	}

	// Download to a temporary file first (don't assume ZIP)
	tmpFile := filePath + ".download"
	if err := os.Remove(tmpFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	c.logger.Infof("Begin downloading %s, with root: %v", tmpFile, hash)
	if err := indexerClient.Download(ctx, hash, tmpFile, true); err != nil {
		err = errors.Wrapf(err, "Error downloading data with root: %v", hash)
		c.logger.Errorf("%v", err)
		return "", err
	}
	// Ensure temp file is cleaned up if we return early with error
	defer func() {
		if _, statErr := os.Stat(tmpFile); statErr == nil {
			if rmErr := os.Remove(tmpFile); rmErr != nil && !os.IsNotExist(rmErr) {
				c.logger.Warnf("Failed to remove temp file %s: %v", tmpFile, rmErr)
			}
		}
	}()

	// Detect file type: check if it's a ZIP by reading the magic bytes
	isZip, err := isZipFile(tmpFile)
	if err != nil {
		c.logger.Warnf("Could not detect file type for %s: %v, treating as raw file", tmpFile, err)
		isZip = false
	}

	if isZip {
		// ZIP file: unzip as before
		topLevelDir, err := util.Unzip(tmpFile, filepath.Dir(filePath))
		if err != nil {
			c.logger.Errorf("Error unzipping data: %v\n", err)
			return "", err
		}
		// Clean up the downloaded zip
		os.Remove(tmpFile)
		c.logger.Infof("Downloaded and unzipped %s", tmpFile)
		return topLevelDir, nil
	}

	// Raw file (e.g., JSONL): move directly to the target path
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return "", err
	}
	// Remove existing target (may exist from a previous attempt, could be a file or directory)
	if err := os.RemoveAll(filePath); err != nil {
		return "", errors.Wrap(err, "remove existing target path before move")
	}
	if err := os.Rename(tmpFile, filePath); err != nil {
		return "", errors.Wrap(err, "move downloaded file to target path")
	}
	c.logger.Infof("Downloaded raw file to %s", filePath)
	return filePath, nil
}

// isZipFile checks if a file is a ZIP archive by reading its magic bytes.
// ZIP files start with the bytes "PK" (0x50, 0x4B, 0x03, 0x04).
func isZipFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 4)
	n, err := f.Read(buf)
	if err != nil {
		return false, fmt.Errorf("failed to read file magic bytes: %w", err)
	}
	if n < 4 {
		// File too small to be a ZIP archive
		return false, nil
	}

	// ZIP magic bytes: PK\x03\x04
	return buf[0] == 0x50 && buf[1] == 0x4B && buf[2] == 0x03 && buf[3] == 0x04, nil
}

func (c *Client) UploadToStorage(ctx context.Context, fileName string, isTurbo bool) ([]ethcommon.Hash, error) {
	finalityRequired := transfer.TransactionPacked
	if c.storageUploadUrgs.FinalityRequired {
		finalityRequired = transfer.FileFinalized
	}

	opt := transfer.UploadOption{
		Tags:             hexutil.MustDecode(c.storageUploadUrgs.Tags),
		FinalityRequired: finalityRequired,
		TaskSize:         c.storageUploadUrgs.TaskSize,
		ExpectedReplica:  c.storageUploadUrgs.ExpectedReplica,
		SkipTx:           c.storageUploadUrgs.SkipTx,
		MaxGasPrice:      c.MaxGasPrice,
		NRetries:         c.NRetries,
		Step:             c.storageUploadUrgs.Step,
		Method:           c.Method,
		FullTrusted:      c.storageUploadUrgs.FullTrusted,
	}

	file, err := core.Open(fileName)
	if err != nil {
		c.logger.Errorf("Error opening file to upload: %v\n", err)
		return nil, err
	}
	defer file.Close()

	var indexerClient *indexer.Client
	if isTurbo {
		indexerClient = c.indexerTurboClient
	} else {
		indexerClient = c.indexerStandardClient
	}

	// In v1.2.2, Routines is set internally via UploaderConfig in NewUploaderFromIndexerNodes
	uploader, err := indexerClient.NewUploaderFromIndexerNodes(ctx, file.NumSegments(), c.w3Client, opt.ExpectedReplica, nil, c.Method, opt.FullTrusted)
	if err != nil {
		c.logger.Errorf("Error creating uploader: %v\n", err)
		return nil, err
	}

	_, roots, err := uploader.SplitableUpload(ctx, file, c.storageUploadUrgs.FragmentSize, opt)

	// Retry with full trusted nodes if initial upload fails and FullTrusted was false
	if err != nil && !opt.FullTrusted {
		c.logger.Warnf("Upload with non-full-trusted nodes failed, retrying with full trusted nodes: %v", err)
		opt.FullTrusted = true

		fullUploader, err := indexerClient.NewUploaderFromIndexerNodes(ctx, file.NumSegments(), c.w3Client, opt.ExpectedReplica, nil, c.Method, true)
		if err != nil {
			c.logger.Errorf("Error creating full trusted uploader: %v\n", err)
			return nil, err
		}

		_, roots, err = fullUploader.SplitableUpload(ctx, file, c.storageUploadUrgs.FragmentSize, opt)
	}

	if err != nil {
		err = errors.Wrapf(err, "Error uploading file: %v", fileName)
		c.logger.Errorf("%v", err)
		return nil, err
	}

	if len(roots) == 1 {
		c.logger.Infof("file uploaded in 1 fragment, root = %v", roots[0].String())
	} else {
		s := make([]string, len(roots))
		for i, root := range roots {
			s[i] = root.String()
		}
		c.logger.Infof("file uploaded in %v fragments, roots = %v", len(roots), s)
	}

	return roots, nil
}
