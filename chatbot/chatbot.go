package chatbot

import (
	"gorm.io/gorm"

	"github.com/0glabs/0g-serving-agent/common/model"
)

// type ChatBotRequest model.Request
type ChatBot struct {
	DB *gorm.DB

	DataFetcherInfo model.DataFetcherInfo
}

func (c *ChatBot) GetDataFetcherInfo() model.DataFetcherInfo {
	return c.DataFetcherInfo
}
