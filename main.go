package main

import (
	"cmp"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html/template"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
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

type Activity struct {
	Start, End time.Time
	Months     []ActivityMonth
	Total      int
	BySender   []SenderActivity
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

type SenderActivity struct {
	Name  String
	Total int
}

func (c Conversation) Activity() Activity {
	start := c.Messages[0].time
	end := c.Messages[len(c.Messages)-1].time

	ys, ms, ds := start.Date()
	ye, me, de := end.Date()

	a := Activity{
		Start:    start,
		End:      end,
		Months:   make([]ActivityMonth, (ye-ys)*12+int(me-ms)+1),
		Total:    len(c.Messages),
		BySender: make([]SenderActivity, 0, len(c.Participants)),
	}
	ambiguous := c.ambiguousParticipants()
	maxCount := 0
	totalBySender := map[String]int{}

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
			totalBySender[msg.Sender]++
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

	for sender, c := range totalBySender {
		a.BySender = append(a.BySender, SenderActivity{
			Name:  sender,
			Total: c,
		})
	}

	for _, sender := range c.Participants {
		if _, ok := totalBySender[sender.Name]; !ok {
			a.BySender = append(a.BySender, SenderActivity{Name: sender.Name})
		}
	}

	slices.SortFunc(a.BySender, func(a, b SenderActivity) int {
		_, amba := ambiguous[a.Name]
		_, ambb := ambiguous[b.Name]

		return cmp.Or(
			cmp.Compare(b.Total, a.Total),
			cmpBool(amba, ambb),
			cmp.Compare(a.Name, b.Name),
		)
	})

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
	parts := strings.Split(r.URI, "/")
	return strings.Join(parts[len(parts)-3:], "/")
}

type Share struct {
	Link                 string `json:"link"`
	ShareText            String `json:"share_text"`
	OriginalContentOwner String `json:"original_content_owner"`
}

func main() {
	const inboxRoot = "your_instagram_activity/messages/inbox"

	profile, err := decodeJSON[PersonalInformation]("personal_information/personal_information/personal_information.json")
	if err != nil {
		panic(err)
	}

	conversations, err := os.ReadDir(inboxRoot)
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

		conv, err := decodeConversation(inboxRoot, conversationID, profile.Name())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		parseExec(w, "conversation.html", conv)
	})

	http.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(inboxRoot, r.URL.Path))
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

func decodeConversation(root, conversationID string, selfName String) (Conversation, error) {
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
		c, err := decodeJSON[Conversation](f)
		if err != nil {
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
			i := strings.Index(m.Share.Link, "instagram.com/stories/")
			if i >= 0 {
				m.Share = nil
				m.Content = "Replied to story"
			}
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
			"add": func(a, b int) int {
				return a + b
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

func (a Activity) Colors() []string {
	var theme = []string{
		"#CC4400",
		"#D66915",
		"#E08E29",
		"#F0C761",
		"#FFFF99",
		"#C2FCFF",
		"#7CC6DE",
		"#3890BC",
		"#1C489A",
		"#000077",
	}

	numColors := 0
	firstTwo := []string(nil)
	for _, s := range a.BySender {
		if s.Total > 0 {
			numColors++
			if numColors <= 2 {
				firstTwo = append(firstTwo, string(s.Name))
			}
		}
	}

	if numColors == 2 {
		h := fnv.New128a()
		io.WriteString(h, firstTwo[0])
		io.WriteString(h, firstTwo[1])
		sum := h.Sum(nil)
		r := rand.New(rand.NewPCG(binary.BigEndian.Uint64(sum), binary.BigEndian.Uint64(sum[8:])))
		i := r.IntN(len(theme) / 2)
		j := r.IntN(len(theme) / 2)
		return []string{theme[2*i], theme[2*j+1]}
	}

	stride := max(1, len(theme)/numColors)
	colors := make([]string, 0, min(numColors, len(theme)))

	for i := 0; i < len(theme); i += stride {
		colors = append(colors, theme[i])
	}

	return colors
}

func cmpBool(a, b bool) int {
	if a == b {
		return 0
	}

	if !a && b {
		return -1
	}

	return 1
}

type PersonalInformation struct {
	ProfileUser []struct {
		StringMapData map[String]struct {
			Href      string `json:"href"`
			Value     String `json:"value"`
			Timestamp int64  `json:"timestamp"`
		} `json:"string_map_data"`
	} `json:"profile_user"`
}

func decodeJSON[T any](f string) (T, error) {
	data, err := os.ReadFile(f)
	if err != nil {
		return *new(T), fmt.Errorf("read: %w", err)
	}

	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return *new(T), fmt.Errorf("unmarshal: %w", err)
	}

	return v, nil
}

func (p PersonalInformation) Name() String {
	for _, u := range p.ProfileUser {
		for key, data := range u.StringMapData {
			if strings.Contains(string(key), "name") {
				return data.Value
			}
		}
	}

	return ""
}
