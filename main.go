package main

import (
	"embed"
	_ "embed"
	"encoding/json/v2"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/gzhttp"
)

const (
	rootMessages            = "your_instagram_activity/messages"
	rootMedia               = "your_instagram_activity/media"
	rootPersonalInformation = "personal_information/personal_information"
	rootInbox               = rootMessages + "/inbox"
)

func main() {
	var selfID, tzName string
	var dev bool

	flag.BoolVar(&dev, "dev", false, "Enable development mode.")
	flag.StringVar(&selfID, "user_id", "", "Your Instagram user ID.")
	flag.StringVar(&tzName, "tz", "Europe/Bucharest", "IANA identifier for desired timezone.")
	flag.Parse()

	root := flag.Arg(0)
	if root == "" {
		root = "."
	}

	loc, err := time.LoadLocation(tzName)
	if err != nil {
		panic(err)
	}

	var profile PersonalInformation
	if err := decodeJSON(filepath.Join(root, rootPersonalInformation, "personal_information.json"), &profile); err != nil {
		panic(err)
	}

	inboxRoot := filepath.Join(root, rootInbox)

	conversations, err := os.ReadDir(inboxRoot)
	if err != nil {
		panic(err)
	}

	conversations = slices.DeleteFunc(conversations, func(e os.DirEntry) bool {
		return !e.IsDir()
	})

	stories, err := decodeStories(root, selfID, profile.Name(), loc)
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		parseExec(w, "list.html", conversations, dev)
	})

	mux.HandleFunc("GET /conversation/{id}/{$}", func(w http.ResponseWriter, r *http.Request) {
		conversationID := r.PathValue("id")
		if conversationID == "favicon.ico" {
			http.NotFound(w, r)
			return
		}

		conv, err := decodeConversation(inboxRoot, conversationID, selfID, profile.Name(), loc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		parseExec(w, "conversation.html", conv, dev)
	})

	mux.Handle("GET /conversation/", http.StripPrefix("/conversation/", http.FileServer(http.Dir(inboxRoot))))
	mux.Handle("GET /media/", http.StripPrefix("/media/", http.FileServer(http.Dir(filepath.Join(root, "media")))))

	mux.HandleFunc("GET /story/{id}/{$}", func(w http.ResponseWriter, r *http.Request) {
		var tmpl struct {
			Story
			MessageID, ConversationID string
		}

		q := r.URL.Query()
		tmpl.ConversationID = q.Get("conv")
		tmpl.MessageID = q.Get("msg")

		story, ok := stories.FindByID(r.PathValue("id"), tmpl.ConversationID)
		if !ok {
			http.NotFound(w, r)
			return
		}

		tmpl.Story = story

		parseExec(w, "story.html", tmpl, dev)
	})

	mux.Handle("/", http.NotFoundHandler())

	log.Fatalln(http.ListenAndServe(":8080", gzhttp.GzipHandler(mux)))
}

//go:embed *.html
var templates embed.FS

func dateEqual(date1, date2 time.Time) bool {
	y1, m1, d1 := date1.Date()
	y2, m2, d2 := date2.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

func daysIn(m time.Month, year int) int {
	return time.Date(year, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func parseExec(w http.ResponseWriter, templatePath string, data any, dev bool) {
	t := template.New(filepath.Base(templatePath)).
		Funcs(template.FuncMap{
			"perc": func(f float64) string {
				return fmt.Sprintf("%.1f%%", f*100)
			},
			"add": func(a, b int) int {
				return a + b
			},
		})

	var err error
	if dev {
		t, err = t.ParseFiles(templatePath)
	} else {
		t, err = t.ParseFS(templates, templatePath)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !dev {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}

	if err := t.Execute(w, data); err != nil {
		fmt.Fprintf(os.Stderr, "render: %v\n", err)
	}
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

func decodeJSON(f string, out any) error {
	data, err := os.ReadFile(f)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	return nil
}
