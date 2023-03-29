package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	blocksURL  = "https://p2pool.io/mini/api/pool/blocks"
	configPath = "./config.toml"
)

var (
	lastBlockChecked       = block{}
	errUnexpectedStructure = errors.New("unexpected response structure")
)

type block struct {
	height int
	ts     time.Time
}

type config struct {
	ApiKey          string `toml:"APIKey"`
	SubscribersFile string `toml:"SubscribersFile"`
	NotifyDuration  string `toml:"NotifyDuration"`
}

func readConfig() (config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return config{}, err
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return config{}, err
	}

	var conf config
	_, err = toml.Decode(string(data), &conf)
	if err != nil {
		return config{}, err
	}

	return conf, nil
}

func main() {
	conf, err := readConfig()
	if err != nil {
		log.Fatal(err)
	}

	bot, err := tgbotapi.NewBotAPI(conf.ApiKey)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = true

	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	notifyDuration, err := time.ParseDuration(conf.NotifyDuration)
	if err != nil {
		log.Fatal(err)
	}

	go worker(context.TODO(), bot, notifyDuration, conf.SubscribersFile)

	for update := range updates {
		if update.Message != nil {
			log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)

			err := saveSubscriberID(update.Message.Chat.ID, conf.SubscribersFile)
			var msg tgbotapi.MessageConfig
			if err != nil {
				msg = tgbotapi.NewMessage(update.Message.Chat.ID, "Ошибка при попытке подписаться на уведомления :c")
			} else {
				msg = tgbotapi.NewMessage(update.Message.Chat.ID, "Вы успешно подписались на обновления! Теперь бот будет присылать вам сообщение с каждым найденным блоком пулом https://p2pool.io/mini/#pool c:")
			}

			msg.ReplyToMessageID = update.Message.MessageID

			bot.Send(msg)
		}
	}
}

func saveSubscriberID(tgid int64, subscribersFilePath string) error {
	file, err := os.OpenFile(subscribersFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(strconv.FormatInt(tgid, 10) + "\n")
	if err != nil {
		return err
	}

	return nil
}

func worker(ctx context.Context, bot *tgbotapi.BotAPI, interval time.Duration, subscribersFilePath string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			err := tryNotifyIfNewBlock(ctx, bot, subscribersFilePath)
			if err != nil {
				log.Printf("error: %s", err.Error())
			}
			time.Sleep(interval)
		}
	}
}

func tryNotifyIfNewBlock(_ context.Context, bot *tgbotapi.BotAPI, subscribersFilePath string) error {
	lastBlock, err := fetchLastBlock()
	if err != nil {
		return err
	}

	if lastBlock.height != lastBlockChecked.height {
		lastBlockChecked = lastBlock
		ids, err := getSubscribers(subscribersFilePath)
		if err != nil {
			return err
		}

		for _, id := range ids {
			msg := tgbotapi.NewMessage(id, fmt.Sprintf("Блок найден! Высота: %d, время: %s", lastBlock.height, lastBlock.ts.Format(time.RFC850)))
			_, err := bot.Send(msg)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func getSubscribers(subscribersFilePath string) ([]int64, error) {
	file, err := os.Open(subscribersFilePath)
	if err != nil {
		var pErr *fs.PathError
		if errors.As(err, &pErr) {
			log.Printf("no subscribers yet, skip")
			return nil, nil
		}
	}
	defer file.Close()

	var ids []int64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		id, err := strconv.ParseInt(scanner.Text(), 10, 64)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return ids, nil
}

func fetchLastBlock() (block, error) {
	res, err := http.Get(blocksURL)
	if err != nil {
		return block{}, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return block{}, err
	}

	var blocks []map[string]interface{}
	err = json.Unmarshal(body, &blocks)
	if err != nil {
		return block{}, err
	}

	if len(blocks) <= 0 {
		return block{}, errUnexpectedStructure
	}

	if _, ok := blocks[0]["height"]; !ok {
		return block{}, errUnexpectedStructure
	}

	if _, ok := blocks[0]["height"].(float64); !ok {
		return block{}, errUnexpectedStructure
	}

	lastHeight := blocks[0]["height"].(float64)

	lastTs, ok := blocks[0]["ts"].(float64)
	if !ok {
		return block{}, errUnexpectedStructure
	}

	lastTime := time.UnixMilli(int64(lastTs))

	return block{
		height: int(lastHeight),
		ts:     lastTime,
	}, nil
}
