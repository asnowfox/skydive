/*
 * Copyright (C) 2019 IBM, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy ofthe License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specificlanguage governing permissions and
 * limitations under the License.
 *
 */

package core

import (
	"github.com/spf13/viper"
)

// Filterer exposes the interface for tag based filtering
type Filterer interface {
	IsExcluded(tag Tag) bool
}

type filter struct {
	excludedTags map[Tag]bool
}

func (f *filter) IsExcluded(tag Tag) bool {
	_, ok := f.excludedTags[tag]
	return ok
}

// NewFilterFromConfig returns a new filter based on config
func NewFilterFromConfig(cfg *viper.Viper) (Filterer, error) {
	return NewFilter(cfg.GetStringSlice(CfgRoot + "filter.excluded_tags")...)
}

// NewFilter returns a new filter based on a list of excluded tags
func NewFilter(tags ...string) (Filterer, error) {
	excludedTags := make(map[Tag]bool)
	for _, t := range tags {
		excludedTags[Tag(t)] = true
	}

	return &filter{excludedTags: excludedTags}, nil
}
