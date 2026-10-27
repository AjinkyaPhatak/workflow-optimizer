package compute

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os/exec"
	"time"

	"github.com/google/uuid"
)

type AnalysisArtifact struct {
	Name string `json:"name"`
	Data string `json:"data"`
}
type AnalysisResult struct {
	Text      string             `json:"text"`
	Artifacts []AnalysisArtifact `json:"artifacts"`
}
type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("analysis output is too large")
	}
	return b.Buffer.Write(p)
}
func Analyze(ctx context.Context, image string, rows any, code string) (AnalysisResult, error) {
	if len(code) > 30000 {
		return AnalysisResult{}, errors.New("analysis instructions are too long")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	name := "workflow-analysis-" + uuid.NewString()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
	}()
	input, err := json.Marshal(map[string]any{"rows": rows, "code": code})
	if err != nil {
		return AnalysisResult{}, err
	}
	if len(input) > 10<<20 {
		return AnalysisResult{}, errors.New("dataset is too large for analysis")
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "--name", name, "--network", "none", "--read-only",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "512m", "--cpus", "1",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=128m,mode=1777", image)
	cmd.Stdin = bytes.NewReader(input)
	out := &boundedBuffer{limit: 20 << 20}
	cmd.Stdout = out
	var stderr boundedBuffer
	stderr.limit = 64000
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return AnalysisResult{}, errors.New("the isolated analysis could not finish; check the dataset, code, or analysis image")
	}
	var result AnalysisResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		return result, errors.New("analysis returned an unreadable result")
	}
	for _, a := range result.Artifacts {
		if _, err := base64.StdEncoding.DecodeString(a.Data); err != nil {
			return AnalysisResult{}, errors.New("analysis returned an invalid file")
		}
	}
	return result, nil
}
