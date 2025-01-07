package ctrl

import (
	"context"

	"github.com/0glabs/0g-storage-client/core"
	"github.com/0glabs/0g-storage-client/indexer"
	"github.com/0glabs/0g-storage-client/transfer"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

func (c *Ctrl) DownloadFromStorage(ctx context.Context, hash, fileName string, isTurbo bool) error {
	if isTurbo {
		if err := c.indexerTurboClient.Download(ctx, hash, fileName, true); err != nil {
			c.logger.Errorf("Error downloading dataset: %v\n", err)
			return err
		}
	} else {
		if err := c.indexerStandardClient.Download(ctx, hash, fileName, true); err != nil {
			c.logger.Errorf("Error downloading dataset: %v\n", err)
			return err
		}
	}
	return nil
}

func (c *Ctrl) UploadToStorage(ctx context.Context, fileName string, isTurbo bool) error {
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
	}

	file, err := core.Open(fileName)
	if err != nil {
		c.logger.Errorf("Error opening file to upload: %v\n", err)
		return err
	}
	defer file.Close()

	var indexerClient *indexer.Client
	if isTurbo {
		indexerClient = c.indexerTurboClient
	} else {
		indexerClient = c.indexerStandardClient
	}

	uploader, err := indexerClient.NewUploaderFromIndexerNodes(ctx, file.NumSegments(), c.w3Client, opt.ExpectedReplica, nil)
	if err != nil {
		c.logger.Errorf("Error creating uploader: %v\n", err)
		return err
	}
	defer indexerClient.Close()

	uploader.WithRoutines(c.storageUploadUrgs.Routines)

	_, roots, err := uploader.SplitableUpload(ctx, file, c.storageUploadUrgs.FragmentSize, opt)
	if err != nil {
		c.logger.Errorf("Error uploading file: %v\n", err)
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

	return nil
}
