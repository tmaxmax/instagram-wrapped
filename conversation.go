package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Conversation struct {
	ID           string        `json:"-"`
	Messages     []Message     `json:"messages"`
	Participants []Participant `json:"participants"`
	Title        String        `json:"title"`
}

func (c Conversation) Dates() [][]Message {
	var date time.Time
	var start int
	var ret [][]Message

	for i, m := range c.Messages {
		c.Messages[i] = m

		if dateEqual(date, m.time) {
			continue
		}

		if i > 0 {
			ret = append(ret, c.Messages[start:i])
		}

		start = i
		date = m.time
	}

	return append(ret, c.Messages[start:])
}

func (c Conversation) Activity() Activity {
	return computeActivity(c)
}

type Message struct {
	Videos    []Resource `json:"videos"`
	Audios    []Resource `json:"audio_files"`
	Photos    []Resource `json:"photos"`
	Share     *Share     `json:"share"`
	Content   String     `json:"content"`
	Sender    String     `json:"sender_name"`
	Timestamp int64      `json:"timestamp_ms"`
	Me        bool       `json:"-"`
	time      time.Time  `json:"-"`
}

type Reaction struct {
	Content   String `json:"reaction"`
	Actor     String `json:"actor"`
	Timestamp int64  `json:"timestamp"`
}

type Participant struct {
	Name String `json:"name"`
}

func (m Message) Date() string {
	return m.time.Format(time.DateOnly)
}

func (m Message) Time() string {
	return m.time.Format(time.TimeOnly)
}

type Resource struct {
	URI string `json:"uri"`
}

func (r Resource) Src() string {
	if uri, ok := strings.CutPrefix(r.URI, rootMessages+"/inbox/"); ok {
		_, path, _ := strings.Cut(uri, "/")
		return path
	}

	return r.URI
}

type Share struct {
	Link                 string `json:"link"`
	ShareText            String `json:"share_text"`
	OriginalContentOwner String `json:"original_content_owner"`
}

// IsStory reports whether this is a link our code has processed as a story.
// This is not part of the Instagram data!
func (s Share) IsStory() bool {
	return strings.HasPrefix(s.Link, "/story")
}

type String string

func (s *String) UnmarshalJSON(data []byte) error {
	var u string
	if err := json.Unmarshal(data, &u); err != nil {
		return err
	}

	*s = String(decodeInstagramString(u))

	return nil
}

func decodeInstagramString(escaped string) string {
	runes := []rune(escaped)
	bytes := make([]byte, len(runes))
	for i, r := range runes {
		bytes[i] = byte(r)
	}
	return string(bytes)
}

func decodeConversation(root, conversationID, selfID string, selfName String, loc *time.Location) (Conversation, error) {
	convFiles, err := filepath.Glob(filepath.Join(root, conversationID, "message_*.json"))
	if err != nil {
		return Conversation{}, fmt.Errorf("find message files: %w", err)
	}

	var conv Conversation

	for _, f := range slices.Backward(convFiles) {
		var c Conversation
		if err := decodeJSON(f, &c); err != nil {
			return Conversation{}, err
		}

		slices.Reverse(c.Messages)

		if conv.Title == "" {
			conv = c
			conv.ID = conversationID
		} else {
			conv.Messages = append(conv.Messages, c.Messages...)
		}
	}

	for i := range conv.Messages {
		m := &conv.Messages[i]
		m.Me = m.Sender == selfName

		if u, err := url.ParseRequestURI(string(m.Content)); err == nil && strings.HasPrefix(u.Scheme, "http") {
			m.Share = &Share{Link: string(m.Content)}
			m.Content = ""
		}

		if m.Share != nil {
			if userID, storyID, ok := parseStoryURL(m.Share.Link); ok {
				if userID == selfID {
					m.Share.Link = fmt.Sprintf("/story/%s?conv=%s", storyID, conversationID)
					m.Share.OriginalContentOwner = selfName
				} else {
					content := String("Replied to story")
					if m.Content != "" {
						content += ": " + m.Content
					}
					m.Share = nil
					m.Content = content
				}
			}
		}

		m.time = time.UnixMilli(m.Timestamp).In(loc)
	}

	return conv, nil
}
