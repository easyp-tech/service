package sdk

import (
	generator "github.com/easyp-tech/service/api/easyp/generator/v1"
)

// PluginFilter задаёт критерии фильтрации плагинов.
// Пустые поля игнорируются (не участвуют в фильтрации).
type PluginFilter struct {
	Group   string
	Name    string
	Version string
	Tags    []string
}

// isEmpty возвращает true, если все поля фильтра пусты.
func (f PluginFilter) isEmpty() bool {
	return f.Group == "" && f.Name == "" && f.Version == "" && len(f.Tags) == 0
}

// containsAll проверяет, что pluginTags содержит все элементы из filterTags.
// Пустые строки в filterTags игнорируются.
func containsAll(pluginTags, filterTags []string) bool {
	set := make(map[string]struct{}, len(pluginTags))
	for _, t := range pluginTags {
		set[t] = struct{}{}
	}

	for _, t := range filterTags {
		if t == "" {
			continue
		}
		if _, ok := set[t]; !ok {
			return false
		}
	}

	return true
}

// applyFilter возвращает плагины, соответствующие всем непустым полям фильтра.
// Пустой фильтр возвращает исходный список без изменений.
func applyFilter(plugins []*generator.PluginInfo, filter PluginFilter) []*generator.PluginInfo {
	if filter.isEmpty() {
		return plugins
	}

	result := make([]*generator.PluginInfo, 0, len(plugins))
	for _, plug := range plugins {
		if filter.Group != "" && plug.GetGroup() != filter.Group {
			continue
		}
		if filter.Name != "" && plug.GetName() != filter.Name {
			continue
		}
		if filter.Version != "" && plug.GetVersion() != filter.Version {
			continue
		}
		if len(filter.Tags) > 0 && !containsAll(plug.GetTags(), filter.Tags) {
			continue
		}
		result = append(result, plug)
	}

	return result
}
