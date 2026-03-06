package main

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"iter"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type StoryMedia struct {
	URI       string    `json:"uri"`
	Title     String    `json:"title"`
	CreatedAt time.Time `json:"-"`
	Timestamp int64     `json:"creation_timestamp"`
}

func (s StoryMedia) IsVideo() bool {
	return strings.HasPrefix(mime.TypeByExtension(filepath.Ext(s.URI)), "video/")
}

func (s StoryMedia) Src() string {
	return "/" + s.URI
}

func (s StoryMedia) Date() string {
	return s.CreatedAt.Format(time.DateOnly)
}

func (s StoryMedia) Time() string {
	return s.CreatedAt.Format(time.TimeOnly)
}

// StoryRef is a reference to a story,
// either through sharing by self or answers
// from others.
type StoryRef struct {
	ConversationID    string
	ConversationTitle String
	UserName          String // is empty when self shared the story
	Time              time.Time
}

type Story struct {
	ID              string
	MediaCandidates []StoryMedia
	Refs            []StoryRef
}

func (s Story) Shares() iter.Seq[StoryRef] {
	return func(yield func(StoryRef) bool) {
		for _, ref := range s.Refs {
			if ref.UserName == "" && !yield(ref) {
				return
			}
		}
	}
}

func (s Story) Answers() iter.Seq[StoryRef] {
	return func(yield func(StoryRef) bool) {
		for _, ref := range s.Refs {
			if ref.UserName != "" && !yield(ref) {
				return
			}
		}
	}
}

func (s Story) NumShares() int {
	count := 0
	for range s.Shares() {
		count++
	}

	return count
}

func (s Story) NumAnswers() int {
	return len(s.Refs) - s.NumShares()
}

type Stories struct {
	refs  map[string][]StoryRef
	media []StoryMedia
}

func (s Stories) FindByID(storyID, refConversationID string) (Story, bool) {
	refs, ok := s.refs[storyID]
	if !ok {
		return Story{}, false
	}

	firstRef := refs[0].Time
	lastRef := firstRef

	for _, ref := range refs[1:] {
		if ref.Time.Sub(firstRef) < 24*time.Hour {
			lastRef = ref.Time
		} else {
			// If there are more references past one day
			// it means this story was in a highlight and lived forever.
			//
			// Not having more references past one day does
			// not imply the story is not in a highlight: for example,
			// one can post a story, not receive any answers in the first day,
			// put it in a highlight and receive an answer after the first day
			// passes. There isn't much we can do about this – answers to
			// or shares of these highlights will be misattributed to more recent
			// stories.
			break
		}
	}

	// Story creation timestamps have second-precision.
	// Round start time down, end time up and make this a non-inclusive interval.
	createdAtStart := lastRef.AddDate(0, 0, -1).Truncate(time.Second)
	createdAtEnd := firstRef.Add(time.Second).Truncate(time.Second)

	// Find interval of stories matching desired timeframe.
	// JSON is in descending order of story creation timestamp.

	i, ok := slices.BinarySearchFunc(s.media, createdAtEnd, func(m StoryMedia, t time.Time) int {
		return t.Compare(m.CreatedAt)
	})
	if ok {
		i++ // interval is non-inclusive, so if we find an exact match we skip it.
	}

	// We could also use binary search for this.
	//
	// Let n be the number of stories posted in a day.
	// Assume n >= log2(total) for half of m, the number of days
	// the account was active, and 0 for the rest.
	// Then total >= m/2*log2(total) => m <= 2*total/log2(total).
	// For a 5 y/o account this would imply 2.5 years of
	// >=14 stories a day.
	//
	// Considering that not many accounts are this active
	// and that binary search comes with additional overhead,
	// for most cases a linear search should suffice.
	j := i
	for j < len(s.media) && s.media[j].CreatedAt.After(createdAtStart) {
		j++
	}

	story := Story{
		ID:              storyID,
		MediaCandidates: slices.Clone(s.media[i:j]),
		Refs:            slices.Clone(refs),
	}

	if len(story.MediaCandidates) == 0 {
		return Story{}, false
	}

	var refConv time.Time
	for _, ref := range refs {
		if ref.ConversationID == refConversationID {
			refConv = ref.Time
		}
	}

	if !refConv.IsZero() {
		slices.SortFunc(story.MediaCandidates, func(a, b StoryMedia) int {
			return cmp.Compare(refConv.Sub(a.CreatedAt), refConv.Sub(b.CreatedAt))
		})
	}

	return story, true
}

func decodeStories(root, selfID string, selfName String, loc *time.Location) (Stories, error) {
	matches, err := filepath.Glob(filepath.Join(root, rootMessages, "*/*/message_*.json"))
	if err != nil {
		return Stories{}, fmt.Errorf("glob: %w", err)
	}

	var files [][]byte
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return Stories{}, err
		}

		files = append(files, data)
	}

	refs := map[string][]StoryRef{}
	for i, f := range files {
		conversationID := filepath.Base(filepath.Dir(matches[i]))
		if err := findReferencesInMessage(f, selfID, conversationID, selfName, refs, loc); err != nil {
			return Stories{}, fmt.Errorf("find references in %s/%s: %w", conversationID, filepath.Base(matches[i]), err)
		}
	}

	for _, ref := range refs {
		slices.SortStableFunc(ref, func(a, b StoryRef) int {
			return a.Time.Compare(b.Time)
		})
	}

	var stories struct {
		Stories []StoryMedia `json:"ig_stories"`
	}
	if err := decodeJSON(filepath.Join(root, rootMedia, "stories.json"), &stories); err != nil {
		return Stories{}, fmt.Errorf("decode stories: %w", err)
	}

	for i, s := range stories.Stories {
		stories.Stories[i].CreatedAt = time.Unix(s.Timestamp, 0).In(loc)
	}

	return Stories{refs: refs, media: stories.Stories}, nil
}

func findReferencesInMessage(buf []byte, selfID, conversationID string, selfName String, ret map[string][]StoryRef, loc *time.Location) error {
	var data struct {
		Title    String `json:"title"`
		Messages []struct {
			TimestampMs int64  `json:"timestamp_ms"`
			SenderName  String `json:"sender_name"`
			Share       struct {
				Link string `json:"link"`
			} `json:"share"`
		} `json:"messages"`
	}

	if err := json.Unmarshal(buf, &data); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	for _, m := range data.Messages {
		userID, storyID, ok := parseStoryURL(m.Share.Link)
		if !ok || userID != selfID {
			continue
		}

		ref := StoryRef{
			ConversationID:    conversationID,
			ConversationTitle: data.Title,
			UserName:          m.SenderName,
			Time:              time.UnixMilli(m.TimestampMs).In(loc),
		}
		if ref.UserName == selfName {
			ref.UserName = ""
		}

		ret[storyID] = append(ret[storyID], ref)
	}

	return nil
}

func parseStoryURL(s string) (userID, storyID string, ok bool) {
	userAndID, ok := strings.CutPrefix(s, "https://www.instagram.com/stories/")
	if !ok {
		return "", "", false
	}

	return strings.Cut(userAndID, "/")
}
