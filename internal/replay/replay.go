package replay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	collogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func BuildFixturePayload(paths []string) ([]byte, error) {
	records := make([]*logsv1.LogRecord, 0, len(paths))
	for _, path := range paths {
		record, err := loadFixtureRecord(path)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}

	request := &collogsv1.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{
			{
				Resource: &resourcev1.Resource{
					Attributes: []*commonv1.KeyValue{
						{
							Key: "service.name",
							Value: &commonv1.AnyValue{
								Value: &commonv1.AnyValue_StringValue{StringValue: "agentgateway"},
							},
						},
					},
				},
				ScopeLogs: []*logsv1.ScopeLogs{
					{
						LogRecords: records,
					},
				},
			},
		},
	}

	payload, err := proto.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal otlp payload: %w", err)
	}
	return payload, nil
}

func loadFixtureRecord(path string) (*logsv1.LogRecord, error) {
	resolvedPath, err := resolveFixturePath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("read fixture %s: %w", resolvedPath, err)
	}

	var record logsv1.LogRecord
	if err := protojson.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decode fixture %s: %w", path, err)
	}
	return &record, nil
}

func resolveFixturePath(path string) (string, error) {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) {
		return clean, nil
	}
	if _, err := os.Stat(clean); err == nil {
		return clean, nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			candidate := filepath.Join(dir, clean)
			if _, statErr := os.Stat(candidate); statErr == nil {
				return candidate, nil
			}
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir || strings.TrimSpace(parent) == "" {
			break
		}
		dir = parent
	}

	return clean, nil
}
