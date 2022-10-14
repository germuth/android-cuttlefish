/*
 * Copyright (C) 2022 The Android Open Source Project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

#pragma once
#include <json/json.h>
#include <iostream>

namespace cuttlefish {

bool ValidateTypo(const Json::Value& root,
                  const std::map<std::string, Json::ValueType>& map);

void InitIntConfig(Json::Value& instances, std::string group,
                   std::string json_flag, int default_value);

void InitStringConfig(Json::Value& instances, std::string group,
                      std::string json_flag, std::string default_value);

void InitStringConfigSubGroup(Json::Value& instances, std::string group,
                              std::string subgroup, std::string json_flag,
                              std::string default_value);

std::string GenerateGflag(const Json::Value& instances, std::string gflag_name,
                          std::string group, std::string json_flag);

std::string GenerateGflagSubGroup(const Json::Value& instances,
                                  std::string gflag_name, std::string group,
                                  std::string subgroup, std::string json_flag);

}  // namespace cuttlefish