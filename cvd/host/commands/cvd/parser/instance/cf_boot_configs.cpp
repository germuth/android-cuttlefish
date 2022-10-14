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
#include "host/commands/cvd/parser/instance/cf_boot_configs.h"

#include <android-base/logging.h>

#include "host/commands/assemble_cvd/flags_defaults.h"
#include "host/commands/cvd/parser/cf_configs_common.h"
#include "host/libs/config/cuttlefish_config.h"

namespace cuttlefish {

static std::map<std::string, Json::ValueType> securitykeyMap = {
    {"serial_number", Json::ValueType::stringValue}};

static std::map<std::string, Json::ValueType> kBootKeyMap = {
    {"extra_bootconfig_args", Json::ValueType::stringValue},
    {"security", Json::ValueType::objectValue}};

bool ValidateSecurityConfigs(const Json::Value& root) {
  if (!ValidateTypo(root, securitykeyMap)) {
    LOG(INFO) << "ValidateSecurityConfigs ValidateTypo fail";
    return false;
  }
  return true;
}

bool ValidateBootConfigs(const Json::Value& root) {
  if (!ValidateTypo(root, kBootKeyMap)) {
    LOG(INFO) << "ValidateBootConfigs ValidateTypo fail";
    return false;
  }

  if (root.isMember("security") && !ValidateSecurityConfigs(root["security"])) {
    LOG(INFO) << "ValidateSecurityConfigs fail";
    return false;
  }

  return true;
}

void InitBootConfigs(Json::Value& instances) {
  InitStringConfig(instances, "boot", "extra_bootconfig_args",
                   CF_DEFAULTS_EXTRA_BOOTCONFIG_ARGS);
  InitStringConfigSubGroup(instances, "boot", "security", "serial_number",
                           CF_DEFAULTS_SERIAL_NUMBER);
}

void GenerateBootConfigs(const Json::Value& instances,
                         std::vector<std::string>& result) {
  result.emplace_back(GenerateGflag(instances, "extra_bootconfig_args", "boot",
                                    "extra_bootconfig_args"));
  result.emplace_back(GenerateGflagSubGroup(instances, "serial_number", "boot",
                                            "security", "serial_number"));
}

}  // namespace cuttlefish