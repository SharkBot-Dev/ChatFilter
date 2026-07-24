package main

import (
	"bufio"
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
	tiktokRegix    = `https?:\/\/lite\.tiktok\.com\/t\/[A-Za-z0-9]+\/?`
	tiktokRe       *regexp.Regexp
	adminPermisson int64 = 8
	ownerId        string
	ruleTypes      []*discordgo.ApplicationCommandOptionChoice = []*discordgo.ApplicationCommandOptionChoice{
		{
			Name:  "Discord招待リンク",
			Value: "invite",
		},
		{
			Name:  "TiktokLiteリンク",
			Value: "tiktok",
		},
		{
			Name:  "スパム",
			Value: "spam",
		},
	}
	spamTextList []string
	commands     = []*discordgo.ApplicationCommand{
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
		{
			Name:        "automod",
			Description: "AutoModを有効化、無効化します。",
			Options: []*discordgo.ApplicationCommandOption{
				{Name: "rule", Type: discordgo.ApplicationCommandOptionString, Description: "有効化するルール", Choices: ruleTypes, Required: true},
				{Name: "enable", Type: discordgo.ApplicationCommandOptionBoolean, Description: "Trueは有効、Falseは無効", Required: true},
			},
			DefaultMemberPermissions: &adminPermisson,
		},
		{
			Name:                     "setting",
			Description:              "設定を表示します。",
			DefaultMemberPermissions: &adminPermisson,
		},
		{
			Name:                     "setup",
			Description:              "Botをセットアップします。",
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

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS automod (
		rule_type TEXT,
		guild_id TEXT
	);`)
	if err != nil {
		log.Fatal(err)
	}
}

func ruleTypeIdToRuleName(ruletype_id string) string {
	for _, ruleType := range ruleTypes {
		if ruleType.Value == ruletype_id {
			return ruleType.Name
		}
	}
	return "不明"
}

func isRuleEnabled(guildId string, ruletype_id string) bool {
	var guild_id string
	err := db.QueryRow("SELECT guild_id FROM automod WHERE guild_id = ? AND rule_type = ?", guildId, ruletype_id).Scan(&guild_id)

	if err == sql.ErrNoRows {
		return false
	}

	return true
}

func includeInviteURL(message *discordgo.MessageCreate) bool {
	if !isRuleEnabled(message.GuildID, "invite") {
		return false
	}

	matches := inviteRe.FindAllString(message.Content, -1)

	if len(matches) == 0 {
		return false
	}

	return true
}

func includeTiktokURL(message *discordgo.MessageCreate) bool {
	if !isRuleEnabled(message.GuildID, "tiktok") {
		return false
	}

	matches := tiktokRe.FindAllString(message.Content, -1)

	if len(matches) == 0 {
		return false
	}

	return true
}

func getWhiteListChannel(guildId string) []string {
	rows, err := db.Query("SELECT channel_id FROM whitelist WHERE guild_id = ?", guildId)

	if err == sql.ErrNoRows {
		return []string{}
	}

	defer rows.Close()

	channels := []string{}

	for rows.Next() {
		var channel_id string

		err := rows.Scan(&channel_id)
		if err != nil {
			log.Fatal(err)
		}

		channels = append(channels, channel_id)
	}

	return channels
}

func getModLogChannel(guildId string) string {
	var channel_id string
	err := db.QueryRow("SELECT channel_id FROM modlog WHERE guild_id = ?", guildId).Scan(&channel_id)

	if err == sql.ErrNoRows {
		return ""
	}

	return channel_id
}

func loadSpamTextChannel() bool {
	file, err := os.Open("data/spam.txt")
	if err != nil {
		log.Print("スパムのロードに失敗しました。")
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		spamTextList = append(spamTextList, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}

	log.Print("スパムのロードをしました。")

	return true
}

func includeSpamText(message *discordgo.MessageCreate) bool {
	if !isRuleEnabled(message.GuildID, "spam") {
		return false
	}

	for _, spam := range spamTextList {
		if strings.Contains(message.Content, spam) {
			return true
		}
	}

	return false
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
	tiktokRe = regexp.MustCompile(tiktokRegix)

	loadSpamTextChannel()

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

					case "reload":
						spamTextList = []string{}
						loadSpamTextChannel()
						s.ChannelMessageSend(message.ChannelID, "必要なテキストをリロードしました。")
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

		var modlog_channel_id = getModLogChannel(message.GuildID)

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

		tiktokInclude := includeTiktokURL(message)
		if tiktokInclude {
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
					Title:       "TiktokLiteリンクが検知されました。",
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
					Title:       "TiktokLiteリンクが検知されました。",
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

		spamInclude := includeSpamText(message)
		if spamInclude {
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
					Title:       "スパムが検知されました。",
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
					Title:       "スパムが検知されました。",
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
									Name:   "スパムの自動削除",
									Value:  "自動でスパムや招待リンクを削除し、\n3分間タイムアウトします。",
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
		case "automod":
			rule_type := i.ApplicationCommandData().GetOption("rule").StringValue()
			enable := i.ApplicationCommandData().GetOption("enable").BoolValue()

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			})

			if enable {
				insertSQL := `INSERT INTO automod (rule_type, guild_id) VALUES (?, ?)`
				_, err = db.Exec(insertSQL, rule_type, i.GuildID)
				if err != nil {
					log.Fatal(err)
				}

				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Embeds: &[]*discordgo.MessageEmbed{
						{
							Title: "ルールを有効化しました。",
							Color: 7005735,
							Fields: []*discordgo.MessageEmbedField{
								{
									Name:   "ルール名",
									Value:  ruleTypeIdToRuleName(rule_type),
									Inline: false,
								},
							},
						},
					},
				})
			} else {
				deleteSQL := `DELETE FROM automod WHERE rule_type = ? AND guild_id = ?`
				_, err = db.Exec(deleteSQL, rule_type, i.GuildID)
				if err != nil {
					log.Fatal(err)
				}

				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Embeds: &[]*discordgo.MessageEmbed{
						{
							Title: "ルールを無効化しました。",
							Color: 7005735,
							Fields: []*discordgo.MessageEmbedField{
								{
									Name:   "ルール名",
									Value:  ruleTypeIdToRuleName(rule_type),
									Inline: false,
								},
							},
						},
					},
				})
			}
		case "setting":
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			})

			whitelist_channels := getWhiteListChannel(i.GuildID)
			modlog_channel := getModLogChannel(i.GuildID)

			whitelist_channels_mention := []string{}
			for _, whitelist_channel := range whitelist_channels {
				whitelist_channels_mention = append(whitelist_channels_mention, "- <#"+whitelist_channel+">")
			}

			enabled_rules := []string{}
			for _, rule := range ruleTypes {
				is_enabled := isRuleEnabled(i.GuildID, rule.Value.(string))
				if is_enabled {
					enabled_rules = append(enabled_rules, "- "+rule.Name)
				}
			}

			fields := []*discordgo.MessageEmbedField{}
			if len(whitelist_channels) != 0 {
				fields = append(fields, &discordgo.MessageEmbedField{
					Name:   "ホワイトリスト一覧",
					Value:  strings.Join(whitelist_channels_mention, "\n"),
					Inline: false,
				})
			} else {
				fields = append(fields, &discordgo.MessageEmbedField{
					Name:   "ホワイトリスト一覧",
					Value:  "設定無し",
					Inline: false,
				})
			}

			if modlog_channel != "" {
				fields = append(fields, &discordgo.MessageEmbedField{
					Name:   "モデレーターログチャンネル",
					Value:  "<#" + modlog_channel + ">",
					Inline: false,
				})
			}

			if len(enabled_rules) != 0 {
				fields = append(fields, &discordgo.MessageEmbedField{
					Name:   "有効なルール一覧",
					Value:  strings.Join(enabled_rules, "\n"),
					Inline: false,
				})
			} else {
				fields = append(fields, &discordgo.MessageEmbedField{
					Name:   "有効なルール一覧",
					Value:  "/automodコマンドで有効化できます。",
					Inline: false,
				})
			}

			s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Embeds: &[]*discordgo.MessageEmbed{
					{
						Title:  "現在の設定一覧です。",
						Color:  7005735,
						Fields: fields,
					},
				},
			})
		case "setup":
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			})

			deleteSQL := `DELETE FROM modlog WHERE guild_id = ?`
			_, err = db.Exec(deleteSQL, i.GuildID)

			// 招待リンク対策を作成
			_, err = db.Exec(`INSERT INTO automod (rule_type, guild_id) VALUES (?, ?)`, "invite", i.GuildID)
			if err != nil {
				log.Fatal(err)
			}

			// tiktok対策
			_, err = db.Exec(`INSERT INTO automod (rule_type, guild_id) VALUES (?, ?)`, "tiktok", i.GuildID)
			if err != nil {
				log.Fatal(err)
			}

			// スパム対策
			_, err = db.Exec(`INSERT INTO automod (rule_type, guild_id) VALUES (?, ?)`, "spam", i.GuildID)
			if err != nil {
				log.Fatal(err)
			}

			// ModLogの作成
			permissionOverwrites := []*discordgo.PermissionOverwrite{
				{
					ID:   i.GuildID,
					Type: discordgo.PermissionOverwriteTypeRole,
					Deny: discordgo.PermissionViewChannel,
				},
			}

			channel, err := s.GuildChannelCreateComplex(i.GuildID, discordgo.GuildChannelCreateData{
				Name:                 "modlog",
				Type:                 discordgo.ChannelTypeGuildText,
				PermissionOverwrites: permissionOverwrites,
			})

			if err != nil {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Embeds: &[]*discordgo.MessageEmbed{
						{
							Title:       "権限がありません。",
							Color:       16711680,
							Description: "権限がないため、\nモデレーターログチャンネルを\n作成できませんでした。",
						},
					},
				})

				return
			}

			// ModLogのセーブ
			_, err = db.Exec(`INSERT INTO modlog (channel_id, guild_id) VALUES (?, ?)`, channel.ID, i.GuildID)
			if err != nil {
				log.Fatal(err)
			}

			s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Embeds: &[]*discordgo.MessageEmbed{
					{
						Title:       "セットアップをしました。",
						Color:       7005735,
						Description: "- すべてのルールを有効化しました。\n- <#" + channel.ID + ">を作成しました。",
					},
				},
			})
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
