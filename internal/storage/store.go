package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"narwhal-monitor/internal/filter"
)

// MonitorState 用于保存上一轮监控快照，防止重启重复刷屏
type MonitorState struct {
	PlanStocks map[string]int `json:"plan_stocks"` // 套餐 ID -> 上次记录的库存数量
}

// Store 管理本地轻量文件存储（规则与状态）
type Store struct {
	mu       sync.Mutex
	dataDir  string
	rulePath string
	statPath string
}

// NewStore 创建本地持久化存储器
func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}

	return &Store{
		dataDir:  dataDir,
		rulePath: filepath.Join(dataDir, "rules.json"),
		statPath: filepath.Join(dataDir, "state.json"),
	}, nil
}

// LoadRules 从 rules.json 加载所有用户的配置与规则
func (s *Store) LoadRules() (map[int64]*filter.ChatConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.rulePath)
	if os.IsNotExist(err) {
		return make(map[int64]*filter.ChatConfig), nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 rules.json 失败: %w", err)
	}

	var configs map[int64]*filter.ChatConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		return nil, fmt.Errorf("解析 rules.json 失败: %w", err)
	}

	if configs == nil {
		configs = make(map[int64]*filter.ChatConfig)
	}
	return configs, nil
}

// SaveRules 将用户的配置与过滤规则原子写入 rules.json
func (s *Store) SaveRules(configs map[int64]*filter.ChatConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化规则失败: %w", err)
	}

	tmpFile := s.rulePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("写入临时规则文件失败: %w", err)
	}

	if err := os.Rename(tmpFile, s.rulePath); err != nil {
		return fmt.Errorf("原子替换 rules.json 失败: %w", err)
	}

	return nil
}

// LoadState 加载监控快照状态
func (s *Store) LoadState() (*MonitorState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.statPath)
	if os.IsNotExist(err) {
		return &MonitorState{
			PlanStocks: make(map[string]int),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 state.json 失败: %w", err)
	}

	var state MonitorState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析 state.json 失败: %w", err)
	}
	if state.PlanStocks == nil {
		state.PlanStocks = make(map[string]int)
	}
	return &state, nil
}

// SaveState 原子保存监控快照状态
func (s *Store) SaveState(state *MonitorState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化状态失败: %w", err)
	}

	tmpFile := s.statPath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("写入临时状态文件失败: %w", err)
	}

	if err := os.Rename(tmpFile, s.statPath); err != nil {
		return fmt.Errorf("原子替换 state.json 失败: %w", err)
	}

	return nil
}
