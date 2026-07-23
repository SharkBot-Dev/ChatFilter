package main

import (
	"database/sql"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"chatfilter/lib"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"

	_ "modernc.org/sqlite"
)

var nowDataFormat = "2006年01月02日 15時04分"
var nowDayFormat = "2006年01月02日"
var nowTimeFormat = "15時04分"

var (
	session        *discordgo.Session
	db             *sql.DB
	dbError        error
	inviteRegix    = `(discord\.(gg|com/invite|app\.com/invite)[/\\][\w-]+)`
	inviteRe       *regexp.Regexp
	adminPermisson int64 = 8
	ownerId        string
	commands       = []*discordgo.ApplicationCommand{
		{
			Name:                     "help",
			Description:              "Botの使い方を知ります",
			DefaultMemberPermissions: &adminPermisson,
		},
		{
			Name:        "whitelist",
			Description: "ホワイトリストに登録します。",
			Options: []*discordgo.ApplicationCommandOption{
				{Name: "action", Type: discordgo.ApplicationCommandOptionString, Description: "実行するアクション", Choices: []*discordgo.ApplicationCommandOptionChoice{
					{
						Name:  "追加する",
						Value: "add",
					},
					{
						Name:  "削除する",
						Value: "remove",
					},
				}, Required: true},
				{Name: "channel", Type: discordgo.ApplicationCommandOptionChannel, Description: "登録するチャンネル", Required: true},
			},
			DefaultMemberPermissions: &adminPermisson,
		},
		{
			Name:        "modlog",
			Description: "モデレーターログチャンネルを指定します。",
			Options: []*discordgo.ApplicationCommandOption{
				{Name: "action", Type: discordgo.ApplicationCommandOptionString, Description: "実行するアクション", Choices: []*discordgo.ApplicationCommandOptionChoice{
					{
						Name:  "設定する",
						Value: "set",
					},
					{
						Name:  "削除する",
						Value: "remove",
					},
				}, Required: true},
				{Name: "channel", Type: discordgo.ApplicationCommandOptionChannel, Description: "登録するチャンネル", Required: true},
			},
			DefaultMemberPermissions: &adminPermisson,
		},
	}
	startTime time.Time
)

func initDB() {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS whitelist (
		channel_id TEXT,
		guild_id TEXT
	);`)
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS modlog (
		channel_id TEXT,
		guild_id TEXT
	);`)
	if err != nil {
		log.Fatal(err)
	}
}

func includeInviteURL(message *discordgo.MessageCreate) bool {
	matches := inviteRe.FindAllString(message.Content, -1)

	if len(matches) == 0 {
		return false
	}

	return true
}

func getModLogChannel(message *discordgo.MessageCreate) string {
	var channel_id string
	err := db.QueryRow("SELECT channel_id FROM modlog WHERE guild_id = ?", message.GuildID).Scan(&channel_id)

	if err == sql.ErrNoRows {
		return ""
	}

	return channel_id
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("環境変数 DISCORD_TOKEN が設定されていません")
	}

	ownerId = os.Getenv("OWNER_ID")
	if ownerId == "" {
		log.Fatal("環境変数 OWNER_ID が設定されていません")
	}

	inviteRe = regexp.MustCompile(inviteRegix)

	db, dbError = sql.Open("sqlite", "./database.db")
	if dbError != nil {
		log.Fatal(dbError)
	}
	defer db.Close()

	initDB()

	sessionManager := &lib.DiscordSessionManager{}
	session = sessionManager.InitializeSession(token)

	session.Identify.Intents |= discordgo.IntentsGuilds
	session.Identify.Intents |= discordgo.IntentsGuildMessages
	session.Identify.Intents |= discordgo.IntentsMessageContent

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Print("起動しました。")

		go func() {
			s.ApplicationCommandBulkOverwrite(s.State.Application.ID, "", commands)
			log.Print("スラッシュコマンドを同期しました。")
		}()

		go func() {
			for {
				s.UpdateCustomStatus("自動掃除中...")
				time.Sleep(10 * time.Second)
			}
		}()
	})

	session.AddHandler(func(s *discordgo.Session, message *discordgo.MessageCreate) {
		if message.Author.Bot {
			return
		}

		if message.GuildID == "" {
			return
		}

		guild, GuildErr := s.State.Guild(message.GuildID)

		if GuildErr != nil {
			return
		}

		// OwnerCommand
		if message.Author.ID == ownerId {
			if strings.HasPrefix(message.Content, "%") {
				content := strings.TrimPrefix(message.Content, "%")
				args := strings.Fields(content)

				if len(args) != 0 {
					command := strings.ToLower(args[0])
					// commandArgs := args[1:]

					switch command {
					case "ping":
						s.ChannelMessageSend(message.ChannelID, "Pong!")

					case "shutdown":
						s.ChannelMessageSend(message.ChannelID, "終了します...")
						session.Close()
						os.Exit(0)
					}
					return
				}
			}
		}

		// オーナーは無視
		if guild.OwnerID == message.Author.ID {
			return
		}

		// ホワイトリストチェック
		var channel_id string
		err := db.QueryRow("SELECT channel_id FROM whitelist WHERE channel_id = ?", message.ChannelID).Scan(&channel_id)

		if err != sql.ErrNoRows {
			return
		}

		var modlog_channel_id = getModLogChannel(message)

		// 招待リンクチェック
		IsIncludeInvite := includeInviteURL(message)
		if IsIncludeInvite {
			messagedelete_err := s.ChannelMessageDelete(message.ChannelID, message.ID)
			if messagedelete_err != nil {
				return
			}

			avatarUrl := ""
			if message.Author.Avatar != "" {
				avatarUrl = "https://cdn.discordapp.com/avatars/" + message.Author.ID + "/" + message.Author.Avatar + ".png"
			} else {
				avatarUrl = "https://cdn.discordapp.com/embed/avatars/0.png"
			}

			// 3分間タイムアウト
			duration_timeout := time.Now().Add(3 * time.Minute)
			timeout_err := s.GuildMemberTimeout(message.GuildID, message.Author.ID, &duration_timeout)

			if timeout_err != nil {
				s.ChannelMessageSendEmbed(modlog_channel_id, &discordgo.MessageEmbed{
					Title:       "タイムアウト権限がありません。",
					Description: "処罰に失敗しました。\nタイムアウト権限を与えてください。",
					Color:       16711680,
					Author: &discordgo.MessageEmbedAuthor{
						Name:    message.Author.Username + " (" + message.Author.ID + ")",
						IconURL: avatarUrl,
					},
				})
			}

			// モデレーターログ
			if modlog_channel_id != "" {
				s.ChannelMessageSendEmbed(modlog_channel_id, &discordgo.MessageEmbed{
					Title:       "招待リンクが検知されました。",
					Description: "メッセージは自動的に削除されました。",
					Color:       16776960,
					Author: &discordgo.MessageEmbedAuthor{
						Name:    message.Author.Username + " (" + message.Author.ID + ")",
						IconURL: avatarUrl,
					},
				})

				if timeout_err == nil {
					s.ChannelMessageSendEmbed(modlog_channel_id, &discordgo.MessageEmbed{
						Title:       "自動でタイムアウトしました。",
						Description: "3分間発言できなくしました。",
						Color:       16711680,
						Author: &discordgo.MessageEmbedAuthor{
							Name:    message.Author.Username + " (" + message.Author.ID + ")",
							IconURL: avatarUrl,
						},
					})
				}
			}

			// 警告
			if timeout_err == nil {
				s.ChannelMessageSendEmbed(message.ChannelID, &discordgo.MessageEmbed{
					Title:       "招待リンクが検知されました。",
					Description: "3分間タイムアウトしました。",
					Color:       16776960,
					Author: &discordgo.MessageEmbedAuthor{
						Name:    message.Author.Username + " (" + message.Author.ID + ")",
						IconURL: avatarUrl,
					},
				})
			}
			return
		}
	})

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}

		commandName := i.ApplicationCommandData().Name

		switch commandName {
		case "help":
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{
						{
							Title: "ChatFilterの使い方",
							Color: 16769280,
							Fields: []*discordgo.MessageEmbedField{
								{
									Name:   "招待リンク削除",
									Value:  "自動で招待リンクを削除し、\n3分間タイムアウトします。",
									Inline: false,
								},
								{
									Name:   "モデレーターログ",
									Value:  "削除された招待リンクを特定のチャンネルに記録します。\n`/modlog`で設定可能です。",
									Inline: false,
								},
								{
									Name:   "ホワイトリスト",
									Value:  "特定のチャンネルでメッセージを削除しないようにします。\n`/whitelist`で設定可能です。",
									Inline: false,
								},
							},
						},
					},
					Flags: discordgo.MessageFlagsEphemeral,
				},
			})
		case "whitelist":
			action := i.ApplicationCommandData().GetOption("action").StringValue()
			channel := i.ApplicationCommandData().GetOption("channel").ChannelValue(s)
			switch action {
			case "add":
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
				})

				insertSQL := `INSERT INTO whitelist (channel_id, guild_id) VALUES (?, ?)`
				_, err = db.Exec(insertSQL, channel.ID, i.GuildID)
				if err != nil {
					log.Fatal(err)
				}

				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Embeds: &[]*discordgo.MessageEmbed{
						{
							Title: "ホワイトリストに登録しました。",
							Color: 7005735,
							Fields: []*discordgo.MessageEmbedField{
								{
									Name:   "チャンネル",
									Value:  "<#" + channel.ID + ">",
									Inline: false,
								},
							},
						},
					},
				})
			case "remove":
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
				})

				deleteSQL := `DELETE FROM whitelist WHERE channel_id = ? AND guild_id = ?`
				_, err = db.Exec(deleteSQL, channel.ID, i.GuildID)
				if err != nil {
					log.Fatal(err)
				}

				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Embeds: &[]*discordgo.MessageEmbed{
						{
							Title: "ホワイトリストから削除しました。",
							Color: 7005735,
							Fields: []*discordgo.MessageEmbedField{
								{
									Name:   "チャンネル",
									Value:  "<#" + channel.ID + ">",
									Inline: false,
								},
							},
						},
					},
				})
			}
		case "modlog":
			action := i.ApplicationCommandData().GetOption("action").StringValue()
			channel := i.ApplicationCommandData().GetOption("channel").ChannelValue(s)
			switch action {
			case "set":
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
				})

				insertSQL := `INSERT INTO modlog (channel_id, guild_id) VALUES (?, ?)`
				_, err = db.Exec(insertSQL, channel.ID, i.GuildID)
				if err != nil {
					log.Fatal(err)
				}

				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Embeds: &[]*discordgo.MessageEmbed{
						{
							Title: "ログチャンネルを登録しました。",
							Color: 7005735,
							Fields: []*discordgo.MessageEmbedField{
								{
									Name:   "チャンネル",
									Value:  "<#" + channel.ID + ">",
									Inline: false,
								},
							},
						},
					},
				})
			case "remove":
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
				})

				deleteSQL := `DELETE FROM modlog WHERE channel_id = ? AND guild_id = ?`
				_, err = db.Exec(deleteSQL, channel.ID, i.GuildID)
				if err != nil {
					log.Fatal(err)
				}

				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Embeds: &[]*discordgo.MessageEmbed{
						{
							Title: "ログチャンネルを削除しました。",
							Color: 7005735,
							Fields: []*discordgo.MessageEmbedField{
								{
									Name:   "チャンネル",
									Value:  "<#" + channel.ID + ">",
									Inline: false,
								},
							},
						},
					},
				})
			}
		}
	})

	if err := session.Open(); err != nil {
		log.Fatalf("Discordセッションのオープンに失敗: %v", err)
	}

	startTime = time.Now()
	defer session.Close()

	log.Println("ボットが起動しました。Ctrl+Cで終了します。")
	waitForExitSignal()
}

func waitForExitSignal() {
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}
