package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoCodeAlone/modular"
	"github.com/GoCodeAlone/workflow/config"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/GoCodeAlone/workflow/plugin"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"
)

// Skill represents a composable skill that can be assigned to agents.
type Skill struct {
	ID            string
	Name          string
	Description   string
	Content       string   // the markdown body (injected into system prompt)
	Category      string   // e.g., "development", "analysis", "communication"
	RequiredTools []string // tools needed for this skill
	CreatedAt     time.Time
}

// skillFrontmatter is the YAML frontmatter structure for skill files.
type skillFrontmatter struct {
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description"`
	Category      string   `yaml:"category"`
	RequiredTools []string `yaml:"required_tools"`
}

// SkillManager manages skills stored in SQLite, loaded from markdown files.
type SkillManager struct {
	db       *sql.DB
	skillDir string
}

// NewSkillManager creates a new SkillManager with the given database and skill directory.
func NewSkillManager(db *sql.DB, skillDir string) *SkillManager {
	return &SkillManager{
		db:       db,
		skillDir: skillDir,
	}
}

// InitTables creates the skills and agent_skills tables.
func (sm *SkillManager) InitTables() error {
	for _, ddl := range []string{createSkillsTable, createAgentSkillsTable} {
		if _, err := sm.db.Exec(ddl); err != nil {
			return fmt.Errorf("skills: create table: %w", err)
		}
	}
	return nil
}

// LoadFromDirectory reads .md files from skillDir and upserts them into the DB.
func (sm *SkillManager) LoadFromDirectory() error {
	entries, err := os.ReadDir(sm.skillDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no skill dir is fine
		}
		return fmt.Errorf("skills: read dir %q: %w", sm.skillDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		fpath := filepath.Join(sm.skillDir, entry.Name())
		data, err := os.ReadFile(fpath)
		if err != nil {
			return fmt.Errorf("skills: read file %q: %w", fpath, err)
		}

		skill, err := parseSkillFile(entry.Name(), data)
		if err != nil {
			return fmt.Errorf("skills: parse %q: %w", fpath, err)
		}

		toolsJSON, err := json.Marshal(skill.RequiredTools)
		if err != nil {
			return fmt.Errorf("skills: marshal tools for %q: %w", skill.ID, err)
		}

		_, err = sm.db.Exec(`
INSERT INTO skills (id, name, description, content, category, required_tools)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    description = excluded.description,
    content = excluded.content,
    category = excluded.category,
    required_tools = excluded.required_tools`,
			skill.ID, skill.Name, skill.Description, skill.Content, skill.Category, string(toolsJSON),
		)
		if err != nil {
			return fmt.Errorf("skills: upsert %q: %w", skill.ID, err)
		}
	}

	return nil
}

// GetSkill retrieves a skill by ID.
func (sm *SkillManager) GetSkill(ctx context.Context, id string) (*Skill, error) {
	row := sm.db.QueryRowContext(ctx,
		"SELECT id, name, description, content, category, required_tools, created_at FROM skills WHERE id = ?", id)
	return scanSkill(row)
}

// ListSkills returns all skills.
func (sm *SkillManager) ListSkills(ctx context.Context) ([]Skill, error) {
	rows, err := sm.db.QueryContext(ctx,
		"SELECT id, name, description, content, category, required_tools, created_at FROM skills ORDER BY name ASC")
	if err != nil {
		return nil, fmt.Errorf("skills: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var skills []Skill
	for rows.Next() {
		s, err := scanSkillRow(rows)
		if err != nil {
			return nil, err
		}
		skills = append(skills, *s)
	}
	return skills, rows.Err()
}

// AssignToAgent assigns a skill to an agent.
func (sm *SkillManager) AssignToAgent(ctx context.Context, agentID, skillID string) error {
	_, err := sm.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO agent_skills (agent_id, skill_id) VALUES (?, ?)",
		agentID, skillID,
	)
	if err != nil {
		return fmt.Errorf("skills: assign to agent %q: %w", agentID, err)
	}
	return nil
}

// RemoveFromAgent removes a skill assignment from an agent.
func (sm *SkillManager) RemoveFromAgent(ctx context.Context, agentID, skillID string) error {
	_, err := sm.db.ExecContext(ctx,
		"DELETE FROM agent_skills WHERE agent_id = ? AND skill_id = ?",
		agentID, skillID,
	)
	if err != nil {
		return fmt.Errorf("skills: remove from agent %q: %w", agentID, err)
	}
	return nil
}

// GetAgentSkills returns all skills assigned to an agent.
func (sm *SkillManager) GetAgentSkills(ctx context.Context, agentID string) ([]Skill, error) {
	rows, err := sm.db.QueryContext(ctx, `
SELECT s.id, s.name, s.description, s.content, s.category, s.required_tools, s.created_at
FROM skills s
JOIN agent_skills ags ON s.id = ags.skill_id
WHERE ags.agent_id = ?
ORDER BY s.name ASC`, agentID)
	if err != nil {
		return nil, fmt.Errorf("skills: get agent skills for %q: %w", agentID, err)
	}
	defer func() { _ = rows.Close() }()

	var skills []Skill
	for rows.Next() {
		s, err := scanSkillRow(rows)
		if err != nil {
			return nil, err
		}
		skills = append(skills, *s)
	}
	return skills, rows.Err()
}

// BuildSkillPrompt concatenates all assigned skill contents for an agent into a prompt section.
func (sm *SkillManager) BuildSkillPrompt(ctx context.Context, agentID string) (string, error) {
	skills, err := sm.GetAgentSkills(ctx, agentID)
	if err != nil {
		return "", err
	}
	if len(skills) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("## Skills\n\n")
	sb.WriteString("You have the following skills available:\n\n")
	for _, s := range skills {
		sb.WriteString("### ")
		sb.WriteString(s.Name)
		sb.WriteString("\n\n")
		sb.WriteString(s.Content)
		sb.WriteString("\n\n")
	}
	return sb.String(), nil
}

// parseSkillFile parses a markdown file with YAML frontmatter into a Skill.
// The filename (without .md) is used as the skill ID.
func parseSkillFile(filename string, data []byte) (*Skill, error) {
	id := strings.TrimSuffix(filename, ".md")
	content := string(data)

	var fm skillFrontmatter
	body := content

	// Check for frontmatter delimited by ---
	if strings.HasPrefix(strings.TrimSpace(content), "---") {
		parts := strings.SplitN(strings.TrimSpace(content), "---", 3)
		// parts[0] is empty (before first ---), parts[1] is YAML, parts[2] is body
		if len(parts) >= 3 {
			if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
				return nil, fmt.Errorf("parse frontmatter: %w", err)
			}
			body = strings.TrimSpace(parts[2])
		}
	}

	name := fm.Name
	if name == "" {
		// Fall back to filename-based name
		name = strings.ReplaceAll(id, "-", " ")
		name = cases.Title(language.English).String(name)
	}

	return &Skill{
		ID:            id,
		Name:          name,
		Description:   fm.Description,
		Content:       body,
		Category:      fm.Category,
		RequiredTools: fm.RequiredTools,
		CreatedAt:     time.Now(),
	}, nil
}

// scanSkill scans a single *sql.Row into a Skill.
func scanSkill(row *sql.Row) (*Skill, error) {
	var s Skill
	var toolsJSON string
	var createdAt string
	if err := row.Scan(&s.ID, &s.Name, &s.Description, &s.Content, &s.Category, &toolsJSON, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("skills: scan: %w", err)
	}
	_ = json.Unmarshal([]byte(toolsJSON), &s.RequiredTools)
	s.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return &s, nil
}

// scanSkillRow scans a *sql.Rows row into a Skill.
func scanSkillRow(rows *sql.Rows) (*Skill, error) {
	var s Skill
	var toolsJSON string
	var createdAt string
	if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Content, &s.Category, &toolsJSON, &createdAt); err != nil {
		return nil, fmt.Errorf("skills: scan row: %w", err)
	}
	_ = json.Unmarshal([]byte(toolsJSON), &s.RequiredTools)
	s.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return &s, nil
}

// skillManagerHook creates a SkillManager and registers it as a service.
func skillManagerHook() plugin.WiringHook {
	return plugin.WiringHook{
		Name:     "ratchet.skill_manager",
		Priority: 70,
		Hook: func(app modular.Application, _ *config.WorkflowConfig) error {
			var db *sql.DB
			if svc, ok := app.SvcRegistry()["ratchet-db"]; ok {
				if dbp, ok := svc.(module.DBProvider); ok {
					db = dbp.DB()
				}
			}
			if db == nil {
				return nil // no DB, skip
			}

			skillDir := "skills"
			if envDir := os.Getenv("RATCHET_SKILLS_DIR"); envDir != "" {
				skillDir = envDir
			}
			sm := NewSkillManager(db, skillDir)

			if err := sm.InitTables(); err != nil {
				return fmt.Errorf("ratchet.skill_manager: init tables: %w", err)
			}
			if err := sm.LoadFromDirectory(); err != nil {
				app.Logger().Warn("ratchet.skill_manager: load from directory failed", "error", err)
			}

			_ = app.RegisterService("ratchet-skill-manager", sm)
			return nil
		},
	}
}
