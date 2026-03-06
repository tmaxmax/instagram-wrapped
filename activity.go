package main

import (
	"cmp"
	"encoding/binary"
	"hash/fnv"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

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

func computeActivity(c Conversation) Activity {
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
	ambiguous := ambiguousParticipants(c)
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

func ambiguousParticipants(c Conversation) map[String]struct{} {
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
