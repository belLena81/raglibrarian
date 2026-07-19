package chunking

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"strconv"
	"strings"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

const TokenizerVersion = "cl100k_base-v1" // #nosec G101 -- public tokenizer identifier, not a credential.

type localBPELoader struct{ path string }

func (l localBPELoader) LoadTiktokenBpe(string) (map[string]int, error) {
	if l.path == "" {
		return nil, errors.New("tokenizer file is required")
	}
	// The library's loader can read local files but does not expose it. Temporarily
	// using its default loader here still keeps the configured path local-only.
	return parseBPEFile(l.path)
}

func parseBPEFile(path string) (map[string]int, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 16<<20 {
		return nil, errors.New("tokenizer asset unavailable")
	}
	contents, err := os.ReadFile(path) // #nosec G304 -- validated operator-owned read-only asset.
	if err != nil {
		return nil, errors.New("tokenizer asset unavailable")
	}
	return parseBPERanks(contents)
}

func parseBPERanks(contents []byte) (map[string]int, error) {
	ranks := make(map[string]int)
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return nil, errors.New("tokenizer asset invalid")
		}
		token, err := base64.StdEncoding.DecodeString(fields[0])
		if err != nil {
			return nil, errors.New("tokenizer asset invalid")
		}
		rank, err := strconv.Atoi(fields[1])
		if err != nil || rank < 0 {
			return nil, errors.New("tokenizer asset invalid")
		}
		ranks[string(token)] = rank
	}
	if err := scanner.Err(); err != nil || len(ranks) == 0 {
		return nil, errors.New("tokenizer asset invalid")
	}
	return ranks, nil
}

type CL100K struct{ encoding *tiktoken.Tiktoken }

func NewCL100K(assetPath string) (*CL100K, error) {
	previous := localBPELoader{path: assetPath}
	tiktoken.SetBpeLoader(previous)
	encoding, err := tiktoken.GetEncoding(tiktoken.MODEL_CL100K_BASE)
	if err != nil {
		return nil, errors.New("tokenizer unavailable")
	}
	return &CL100K{encoding: encoding}, nil
}

func (t *CL100K) Encode(value string) []int  { return t.encoding.EncodeOrdinary(value) }
func (t *CL100K) Decode(tokens []int) string { return t.encoding.Decode(tokens) }
