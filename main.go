package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lrstanley/go-ytdlp"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

func main() {
	dbLog := waLog.Stdout("Database", "DEBUG", true)
	// Initialize SQLite store
	container, err := sqlstore.New(context.Background(), "sqlite", "file:store.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", dbLog)
	if err != nil {
		panic(err)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(err)
	}

	clientLog := waLog.Stdout("Client", "DEBUG", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)

	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			msgText := v.Message.GetConversation()
			if msgText == "" && v.Message.GetExtendedTextMessage() != nil {
				msgText = v.Message.GetExtendedTextMessage().GetText()
			}
			fmt.Printf("Received a message from %s: %s\n", v.Info.Sender.User, msgText)

			if msgText == "ping" && !v.Info.IsFromMe {
				_, err := client.SendMessage(context.Background(), v.Info.Chat, &waE2E.Message{
					Conversation: proto.String("pong"),
				})
				if err != nil {
					fmt.Printf("Error sending message: %v\n", err)
				}
			} else if strings.HasPrefix(strings.ToLower(msgText), "play ") && !v.Info.IsFromMe {
				url := strings.TrimSpace(msgText[5:])
				if url != "" {
					fmt.Printf("Mendownload audio dari %s\n", url)
					err := sendYouTubeAudio(client, v.Info.Chat, url)
					if err != nil {
						fmt.Printf("Error sending audio: %v\n", err)
						client.SendMessage(context.Background(), v.Info.Chat, &waE2E.Message{
							Conversation: proto.String(fmt.Sprintf("Gagal memutar lagu: %v", err)),
						})
					}
				}
			}
		}
	})

	if client.Store.ID == nil {
		// New login
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			panic(err)
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			} else {
				fmt.Println("Login event:", evt.Event)
			}
		}
	} else {
		// Already logged in
		err = client.Connect()
		if err != nil {
			panic(err)
		}
	}

	fmt.Println("Bot is running. Press CTRL+C to exit.")

	// Keep the program running
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	client.Disconnect()
}

func sendYouTubeAudio(client *whatsmeow.Client, chat types.JID, url string) error {
	tmpFile := "temp_audio.m4a"
	os.Remove(tmpFile) // Hapus file lama jika ada

	ytdlp.MustInstall(context.TODO(), nil)
	dl := ytdlp.New().
		Format("bestaudio[ext=m4a]/bestaudio").
		Output(tmpFile).
		NoPlaylist()

	_, err := dl.Run(context.TODO(), url)
	if err != nil {
		return fmt.Errorf("gagal mendownload lagu: %v", err)
	}
	defer os.Remove(tmpFile)

	audioBytes, err := os.ReadFile(tmpFile)
	if err != nil {
		return fmt.Errorf("gagal membaca file audio: %v", err)
	}

	uploaded, err := client.Upload(context.Background(), audioBytes, whatsmeow.MediaAudio)
	if err != nil {
		return err
	}

	msg := &waE2E.Message{
		AudioMessage: &waE2E.AudioMessage{
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			Mimetype:      proto.String("audio/mp4"),
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uint64(len(audioBytes))),
			PTT:           proto.Bool(false),
		},
	}

	_, err = client.SendMessage(context.Background(), chat, msg)
	return err
}
