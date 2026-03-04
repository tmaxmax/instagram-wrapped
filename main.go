package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Conversation struct {
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

type Activity struct {
	Start, End time.Time
	Months     []ActivityMonth
	Total      int
}

type ActivityMonth struct {
	Total    ActivityCount
	BySender map[String]ActivityCount
	month    time.Time
}

func (a ActivityMonth) Year() int {
	y, _, _ := a.month.Date()
	return y
}

func (a ActivityMonth) Month() time.Month {
	_, m, _ := a.month.Date()
	return m
}

func (a ActivityMonth) Date() string {
	return a.month.Format(time.DateOnly)
}

type ActivityCount struct {
	Abs  int
	Perc float64
	Norm float64
}

func (c Conversation) Activity() Activity {
	start := c.Messages[0].time
	end := c.Messages[len(c.Messages)-1].time

	ys, ms, ds := start.Date()
	ye, me, de := end.Date()

	a := Activity{
		Start:  start,
		End:    end,
		Months: make([]ActivityMonth, (ye-ys)*12+int(me-ms)+1),
		Total:  len(c.Messages),
	}
	ambiguous := c.ambiguousParticipants()
	maxCount := 0

	for _, msg := range c.Messages {
		y, m, _ := msg.time.Date()
		mt := &a.Months[(y-ys)*12+int(m-ms)]

		if mt.month.IsZero() {
			mt.month = msg.time
		}

		mt.Total.Abs++
		maxCount = max(maxCount, mt.Total.Abs)

		if mt.BySender == nil {
			mt.BySender = map[String]ActivityCount{}
		}

		if _, ok := ambiguous[msg.Sender]; !ok {
			c := mt.BySender[msg.Sender]
			c.Abs++
			mt.BySender[msg.Sender] = c
		}
	}

	normalize := func(n, days int) float64 {
		return math.Min(1, float64(n*30)/float64(maxCount*days))
	}

	for i := range a.Months {
		d := start.AddDate(0, i, 0)
		y, m, _ := d.Date()

		numDays := daysIn(m, y)
		if i == 0 {
			numDays += -ds + 1
		} else if i == len(a.Months)-1 {
			numDays = de
		}

		mt := &a.Months[i]
		mt.Total.Perc = float64(mt.Total.Abs) / float64(a.Total)
		mt.Total.Norm = normalize(mt.Total.Abs, numDays)

		for sender, c := range mt.BySender {
			c.Perc = float64(c.Abs) / float64(a.Total)
			c.Norm = float64(c.Abs) / float64(mt.Total.Abs)
			mt.BySender[sender] = c
		}

		if mt.month.IsZero() {
			mt.month = d
		}
	}

	return a
}

func (c Conversation) ambiguousParticipants() map[String]struct{} {
	seen := map[String]struct{}{}
	ambiguous := map[String]struct{}{}

	for _, p := range c.Participants {
		if _, ok := seen[p.Name]; ok {
			ambiguous[p.Name] = struct{}{}
		} else {
			seen[p.Name] = struct{}{}
		}
	}

	return ambiguous
}

type Message struct {
	Videos    []Resource `json:"videos"`
	Audios    []Resource `json:"audio_files"`
	Photos    []Resource `json:"photos"`
	Share     *Share     `json:"share"`
	Content   String     `json:"content"`
	Sender    String     `json:"sender_name"`
	Timestamp int64      `json:"timestamp_ms"`
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

func (m Message) Me() bool {
	return m.Sender == "teoxm"
}

type Resource struct {
	URI string `json:"uri"`
}

func (r Resource) Src() string {
	parts := strings.Split(r.URI, "/")
	return strings.Join(parts[len(parts)-3:], "/")
}

type Share struct {
	Link                 string `json:"link"`
	ShareText            String `json:"share_text"`
	OriginalContentOwner String `json:"original_content_owner"`
}

func main() {
	root := os.Args[1]
	if len(os.Args) >= 3 {
		conv, err := decodeConversation(root, os.Args[2])
		if err != nil {
			panic(err)
		}

		activity := conv.Activity()

		fmt.Print("Participants: ")
		for i, p := range conv.Participants {
			if i > 0 {
				fmt.Print(", ")
			}

			fmt.Print(p.Name)
		}
		fmt.Printf("\nTotal activity: %d messages\n", activity.Total)

		fmt.Printf("From %s to %s:", activity.Start.Format("2006-01"), activity.End.Format("2006-01"))
		for i, mt := range activity.Months {
			if i%6 == 0 {
				fmt.Println()
			}

			if mt.Total.Abs == 0 {
				fmt.Print("  -")
				continue
			}

			fmt.Printf("  %.1f%% (%d: ", mt.Total.Perc*100, mt.Total.Abs)

			for i, p := range conv.Participants {
				if i > 0 {
					fmt.Print("/")
				}

				if c, ok := mt.BySender[p.Name]; ok {
					fmt.Printf("%.0f", c.Norm*100)
				}
			}

			fmt.Print(")")
		}
		fmt.Println()

		return
	}

	conversations, err := os.ReadDir(root)
	if err != nil {
		panic(err)
	}

	conversations = slices.DeleteFunc(conversations, func(e os.DirEntry) bool {
		return !e.IsDir()
	})

	http.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		parseExec(w, "list.html", conversations)
	})

	http.HandleFunc("GET /{conversation}/{$}", func(w http.ResponseWriter, r *http.Request) {
		conversationID := r.PathValue("conversation")
		if conversationID == "favicon.ico" {
			http.NotFound(w, r)
			return
		}

		conv, err := decodeConversation(root, conversationID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		parseExec(w, "conversation.html", conv)
	})

	http.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(root, r.URL.Path))
	})

	log.Fatalln(http.ListenAndServe(":8080", nil))
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

func decodeConversation(root, conversationID string) (Conversation, error) {
	loc, err := time.LoadLocation("Europe/Bucharest")
	if err != nil {
		return Conversation{}, fmt.Errorf("load location: %w", err)
	}

	convFiles, err := filepath.Glob(filepath.Join(root, conversationID, "message_") + "*.json")
	if err != nil {
		return Conversation{}, fmt.Errorf("find message files: %w", err)
	}

	var conv Conversation

	for _, f := range slices.Backward(convFiles) {
		data, err := os.ReadFile(f)
		if err != nil {
			return Conversation{}, fmt.Errorf("read file: %w", err)
		}

		var c Conversation
		if err := json.Unmarshal(data, &c); err != nil {
			return Conversation{}, fmt.Errorf("unmarshal: %w", err)
		}

		slices.Reverse(c.Messages)

		if conv.Title == "" {
			conv = c
		} else {
			conv.Messages = append(conv.Messages, c.Messages...)
		}
	}

	for i := range conv.Messages {
		m := &conv.Messages[i]

		if u, err := url.ParseRequestURI(string(m.Content)); err == nil && u.Scheme != "" {
			m.Share = &Share{Link: string(m.Content)}
			m.Content = ""
		}

		m.time = time.UnixMilli(m.Timestamp).In(loc)
	}

	return conv, nil
}

func dateEqual(date1, date2 time.Time) bool {
	y1, m1, d1 := date1.Date()
	y2, m2, d2 := date2.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

func daysIn(m time.Month, year int) int {
	return time.Date(year, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func parseExec(w http.ResponseWriter, templatePath string, data any) {
	t, err := template.New(filepath.Base(templatePath)).
		Funcs(template.FuncMap{
			"perc": func(f float64) string {
				return fmt.Sprintf("%.1f%%", f*100)
			},
		}).
		ParseFiles(templatePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		fmt.Fprintf(os.Stderr, "render: %v\n", err)
	}
}
