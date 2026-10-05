// Package payload compiles templates once and reuses a private buffer per client.
package payload

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

type Spec struct {
	Type, Value string
	Size        int
}
type Variables struct {
	ClientID, Worker string
	Sequence         int
	Seed             uint64
}
type choice struct {
	value  string
	weight float64
}
type part struct {
	literal   string
	kind      byte
	low, high float64
	choices   []choice
}
type Generator struct {
	spec         Spec
	parts        []part
	buffer       []byte
	rng          *rand.Rand
	counter      uint64
	seed1, seed2 uint64
}

func (g *Generator) MaxSize() int { return cap(g.buffer) }

func New(s Spec, v Variables) (*Generator, error) {
	if s.Size < 0 || s.Size > 16<<20 {
		return nil, errors.New("payload size must be between zero and 16 MiB")
	}
	if s.Type == "" {
		s.Type = "static"
	}
	if s.Type != "static" && s.Type != "json" && s.Type != "random" {
		return nil, errors.New("unsupported payload type")
	}
	if s.Type == "json" && s.Value == "" {
		s.Value = `{"deviceId":` + strconv.Quote(v.ClientID) + `,"timestamp":"${timestamp}","sequence":${counter}}`
	}
	s.Value = strings.NewReplacer("${clientId}", v.ClientID, "${worker}", v.Worker, "${sequence}", strconv.Itoa(v.Sequence)).Replace(s.Value)
	seed1 := v.Seed + uint64(v.Sequence)*0x9e3779b97f4a7c15
	seed2 := seed1 ^ 0xda3e39cb94b95bdb
	g := &Generator{spec: s, seed1: seed1, seed2: seed2, rng: rand.New(rand.NewPCG(seed1, seed2))}
	if s.Type == "random" {
		if s.Size == 0 {
			s.Size = 128
			g.spec.Size = s.Size
		}
		g.buffer = make([]byte, s.Size)
		return g, nil
	}
	raw := s.Value
	maxSize := 0
	for len(raw) > 0 {
		start := strings.Index(raw, "${")
		if start < 0 {
			g.parts = append(g.parts, part{literal: raw})
			maxSize += len(raw)
			break
		}
		if start > 0 {
			g.parts = append(g.parts, part{literal: raw[:start]})
			maxSize += start
		}
		end := strings.Index(raw[start:], "}")
		if end < 0 {
			return nil, errors.New("unterminated payload variable")
		}
		token := raw[start+2 : start+end]
		p := part{}
		width := 0
		switch {
		case token == "counter":
			p.kind = 1
			width = 20
		case token == "timestamp":
			p.kind = 2
			width = 30
		case token == "uuid":
			p.kind = 5
			width = 36
		case strings.HasPrefix(token, "random.float:") || strings.HasPrefix(token, "random.int:"):
			fields := strings.Split(token, ":")
			if len(fields) != 3 {
				return nil, errors.New("random variable requires lower and upper bounds")
			}
			lo, e1 := strconv.ParseFloat(fields[1], 64)
			hi, e2 := strconv.ParseFloat(fields[2], 64)
			if e1 != nil || e2 != nil || math.IsNaN(lo) || math.IsNaN(hi) || math.IsInf(lo, 0) || math.IsInf(hi, 0) || lo > hi || math.Abs(lo) > 1e12 || math.Abs(hi) > 1e12 {
				return nil, errors.New("invalid random range")
			}
			p.kind = 3
			if fields[0] == "random.int" {
				p.kind = 4
				if lo != math.Trunc(lo) || hi != math.Trunc(hi) {
					return nil, errors.New("integer range requires integers")
				}
			}
			p.low = lo
			p.high = hi
			width = 20
		case strings.HasPrefix(token, "weighted:"):
			p.kind = 6
			for _, field := range strings.Split(strings.TrimPrefix(token, "weighted:"), ",") {
				pair := strings.SplitN(field, "=", 2)
				if len(pair) != 2 || pair[0] == "" || strings.ContainsAny(pair[0], "\"\\\n\r\t") {
					return nil, errors.New("weighted values require value=weight pairs")
				}
				weight, err := strconv.ParseFloat(pair[1], 64)
				if err != nil || weight <= 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
					return nil, errors.New("weighted value weight must be finite and positive")
				}
				p.high += weight
				if math.IsInf(p.high, 0) {
					return nil, errors.New("weighted sum overflow")
				}
				p.choices = append(p.choices, choice{pair[0], weight})
				width = max(width, len(pair[0]))
			}
		default:
			return nil, errors.New("unknown payload variable")
		}
		g.parts = append(g.parts, p)
		maxSize += width
		raw = raw[start+end+1:]
	}
	g.buffer = make([]byte, 0, max(maxSize, s.Size))
	sample := g.Next()
	if s.Size > 0 && maxSize > s.Size {
		return nil, errors.New("payload size is smaller than maximum expanded template")
	}
	if s.Type == "json" && !json.Valid(sample) {
		return nil, errors.New("expanded JSON template is invalid")
	}
	g.counter = 0
	g.rng = rand.New(rand.NewPCG(seed1, seed2))
	return g, nil
}

// Next returns bytes valid until the next call. A client must finish publishing
// before reusing this buffer. Random values are deterministic, not cryptographic.
func (g *Generator) Next() []byte {
	if g.spec.Type == "random" {
		for i := 0; i < len(g.buffer); i += 8 {
			n := g.rng.Uint64()
			if i+8 <= len(g.buffer) {
				binary.LittleEndian.PutUint64(g.buffer[i:i+8], n)
			} else {
				for j := i; j < len(g.buffer); j++ {
					g.buffer[j] = byte(n)
					n >>= 8
				}
			}
		}
		return g.buffer
	}
	g.counter++
	g.buffer = g.buffer[:0]
	for _, p := range g.parts {
		switch p.kind {
		case 0:
			g.buffer = append(g.buffer, p.literal...)
		case 1:
			g.buffer = strconv.AppendUint(g.buffer, g.counter, 10)
		case 2:
			g.buffer = time.Now().UTC().AppendFormat(g.buffer, time.RFC3339Nano)
		case 3:
			g.buffer = strconv.AppendFloat(g.buffer, p.low+g.rng.Float64()*(p.high-p.low), 'f', 2, 64)
		case 4:
			g.buffer = strconv.AppendInt(g.buffer, int64(p.low)+g.rng.Int64N(int64(p.high-p.low)+1), 10)
		case 5:
			var b [16]byte
			binary.LittleEndian.PutUint64(b[:8], g.rng.Uint64())
			binary.LittleEndian.PutUint64(b[8:], g.rng.Uint64())
			b[6] = (b[6] & 15) | 64
			b[8] = (b[8] & 63) | 128
			var encoded [32]byte
			hex.Encode(encoded[:], b[:])
			g.buffer = append(g.buffer, encoded[:8]...)
			g.buffer = append(g.buffer, '-')
			g.buffer = append(g.buffer, encoded[8:12]...)
			g.buffer = append(g.buffer, '-')
			g.buffer = append(g.buffer, encoded[12:16]...)
			g.buffer = append(g.buffer, '-')
			g.buffer = append(g.buffer, encoded[16:20]...)
			g.buffer = append(g.buffer, '-')
			g.buffer = append(g.buffer, encoded[20:]...)
		case 6:
			pick := g.rng.Float64() * p.high
			selected := p.choices[len(p.choices)-1].value
			for _, c := range p.choices {
				pick -= c.weight
				if pick < 0 {
					selected = c.value
					break
				}
			}
			g.buffer = append(g.buffer, selected...)
		}
	}
	if g.spec.Size > len(g.buffer) {
		pad := byte(0)
		if g.spec.Type == "json" {
			pad = ' '
		}
		for len(g.buffer) < g.spec.Size {
			g.buffer = append(g.buffer, pad)
		}
	}
	return g.buffer
}
