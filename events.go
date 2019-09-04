package autodelete

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/pkg/errors"
)

type userAgentSetter struct {
	t http.RoundTripper
}

func (u *userAgentSetter) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", "AutoDelete (https://github.com/riking/AutoDelete, v1.4)")
	return u.t.RoundTrip(req)
}

func (b *Bot) ConnectDiscord(shardID, shardCount int) error {
	s, err := discordgo.New("Bot " + b.BotToken)
	if err != nil {
		return err
	}
	b.s = s

	// Configure the HTTP client
	runtimeCookieJar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	transport := &userAgentSetter{t: http.DefaultTransport}
	s.Client = &http.Client{
		Timeout:   20 * time.Second,
		Jar:       runtimeCookieJar,
		Transport: transport,
	}

	gb, err := s.GatewayBot()
	if err != nil {
		return err
	}
	fmt.Println("shard count recommendation: ", gb.Shards)
	if shardCount*2 < gb.Shards {
		return errors.Errorf("need to increase shard count: have %d, want %d", shardCount, gb.Shards)
	}

	s.ShardID = shardID
	s.ShardCount = shardCount

	// Add event handlers
	s.AddHandler(b.OnReady)
	s.AddHandler(b.OnResume)
	s.AddHandler(b.OnChannelCreate)
	s.AddHandler(b.OnChannelPins)
	s.AddHandler(b.HandleMentions)
	s.AddHandler(b.OnMessage)
	me, err := s.User("@me")
	if err != nil {
		return errors.Wrap(err, "get me")
	}
	b.me = me

	err = s.Open()
	if err != nil {
		return errors.Wrap(err, "open socket")
	}
	return nil
}

func (b *Bot) HandleMentions(s *discordgo.Session, m *discordgo.MessageCreate) {
	found := false
	for _, v := range m.Message.Mentions {
		if v.ID == b.me.ID {
			found = true
			break
		}
	}
	// TODO allow mentioning the bot role
	// for _, roleID := range m.Message.MentionRoles {
	// looks like <&1234roleid6789>
	// }
	if !found {
		return
	}

	split := strings.Fields(m.Message.Content)
	plainMention := "<@" + b.me.ID + ">"
	nickMention := "<@!" + b.me.ID + ">"

	ch, guild := b.GetMsgChGuild(m.Message)
	if guild == nil {
		fmt.Printf("[ cmd] got mention from %s (%s#%s) in unknown channel %s: %s\n",
			m.Author.Mention(), m.Author.Username, m.Author.Discriminator,
			m.Message.ChannelID, m.Message.Content)
		return
	}

	if ((split[0] == plainMention) ||
		(split[0] == nickMention)) && len(split) > 1 {
		cmd := split[1]
		fun, ok := commands[cmd]
		if ok {
			fmt.Printf("[ cmd] got command from %s (%s#%s) in %s (id %s) guild %s (id %s):\n  %v\n",
				m.Message.Author.Mention(), m.Message.Author.Username, m.Message.Author.Discriminator,
				ch.Name, ch.ID, guild.Name, guild.ID,
				split)
			go fun(b, m.Message, split[2:])
			return
		}
	}
	fmt.Printf("[ cmd] got non-command from %s (%s#%s) in %s (id %s) guild %s (id %s):\n  %s\n",
		m.Message.Author.Mention(), m.Message.Author.Username, m.Message.Author.Discriminator,
		ch.Name, ch.ID, guild.Name, guild.ID,
		m.Message.Content)
}

func (b *Bot) OnMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	b.mu.RLock()
	mCh, ok := b.channels[m.Message.ChannelID]
	b.mu.RUnlock()

	if !ok {
		b.loadChannel(m.Message.ChannelID)
		b.mu.RLock()
		mCh = b.channels[m.Message.ChannelID]
		b.mu.RUnlock()
	}

	if mCh != nil {
		mCh.AddMessage(m.Message)
	}
}

func (b *Bot) OnChannelCreate(s *discordgo.Session, ch *discordgo.ChannelCreate) {
	// No action, need a config message
}

func (b *Bot) OnChannelPins(s *discordgo.Session, ev *discordgo.ChannelPinsUpdate) {
	b.mu.RLock()
	mCh, ok := b.channels[ev.ChannelID]
	b.mu.RUnlock()
	if !ok || mCh == nil {
		return
	}

	disCh, err := s.Channel(ev.ChannelID)
	if err != nil {
		fmt.Println("[pins] error fetching channel:", err)
		return
	}
	if ev.LastPinTimestamp == "" {
		disCh.LastPinTimestamp = nil
	} else {
		var ts = discordgo.Timestamp(ev.LastPinTimestamp)
		disCh.LastPinTimestamp = &ts
	}
	fmt.Println("[pins] got pins update for", mCh.Channel.ID, mCh.Channel.Name, "- new lpts", ev.LastPinTimestamp)
	mCh.UpdatePins(ev.LastPinTimestamp)
}

func (b *Bot) OnReady(s *discordgo.Session, m *discordgo.Ready) {
	b.ReportToLogChannel("AutoDelete started.")
	err := s.UpdateStatus(0, b.GameStatusMsg)
	if err != nil {
		fmt.Println("error setting game:", err)
	}

	go func() {
		err := b.LoadChannelConfigs()
		if err != nil {
			fmt.Println("error loading configs:", err)
		}
	}()
}

func (b *Bot) OnResume(s *discordgo.Session, r *discordgo.Resumed) {
	fmt.Println("Reconnected!")
	go func() {
		time.Sleep(3 * time.Second)
		b.LoadAllBacklogs()
	}()
	go s.UpdateStatus(0, b.GameStatusMsg)
}
