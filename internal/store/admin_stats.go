package store

import (
	"sort"

	"github.com/exitmesh/skills/internal/model"
)

type statsRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanSkillStats(rows statsRows) (model.AdminStats, map[string]int, error) {
	stats := model.AdminStats{Skills: make([]model.SkillStats, 0)}
	positions := make(map[string]int)
	for rows.Next() {
		var skill model.SkillStats
		if err := rows.Scan(&skill.ID, &skill.Name, &skill.Source, &skill.Slug, &skill.SecurityScore, &skill.QualityScore, &skill.LLMChecked); err != nil {
			return model.AdminStats{}, nil, err
		}
		if !skill.LLMChecked {
			skill.SecurityScore = 0
			skill.QualityScore = 0
		}
		skill.Clients = make([]model.ClientStats, 0)
		positions[skill.ID] = len(stats.Skills)
		stats.Skills = append(stats.Skills, skill)
	}
	if err := rows.Err(); err != nil {
		return model.AdminStats{}, nil, err
	}
	stats.TotalSkills = len(stats.Skills)
	return stats, positions, nil
}

func addClientStats(stats *model.AdminStats, positions map[string]int, uniqueClients map[string]struct{}, skillID string, client model.ClientStats) {
	position, exists := positions[skillID]
	if !exists {
		return
	}
	skill := &stats.Skills[position]
	skill.Clients = append(skill.Clients, client)
	skill.Downloads += client.Downloads
	skill.UniqueClients++
	last := client.LastDownloadedAt.UTC()
	if skill.LastDownloadedAt == nil || last.After(*skill.LastDownloadedAt) {
		skill.LastDownloadedAt = &last
	}
	stats.TotalDownloads += client.Downloads
	uniqueClients[client.ID] = struct{}{}
}

func finishAdminStats(stats *model.AdminStats, uniqueClients map[string]struct{}) {
	stats.UniqueClients = len(uniqueClients)
	sort.Slice(stats.Skills, func(i, j int) bool {
		if stats.Skills[i].Downloads != stats.Skills[j].Downloads {
			return stats.Skills[i].Downloads > stats.Skills[j].Downloads
		}
		return stats.Skills[i].ID < stats.Skills[j].ID
	})
	for position := range stats.Skills {
		sort.Slice(stats.Skills[position].Clients, func(i, j int) bool {
			left, right := stats.Skills[position].Clients[i], stats.Skills[position].Clients[j]
			if left.Downloads != right.Downloads {
				return left.Downloads > right.Downloads
			}
			return left.ID < right.ID
		})
	}
}
