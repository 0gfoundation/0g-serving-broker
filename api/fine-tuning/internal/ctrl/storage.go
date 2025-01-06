package ctrl

import "context"

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
