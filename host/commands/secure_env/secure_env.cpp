//
// Copyright (C) 2020 The Android Open Source Project
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

#include <android-base/logging.h>
#include <gflags/gflags.h>
#include <keymaster/android_keymaster.h>
#include <keymaster/contexts/pure_soft_keymaster_context.h>
#include <tss2/tss2_esys.h>
#include <tss2/tss2_rc.h>

#include "common/libs/fs/shared_fd.h"
#include "common/libs/security/keymaster_channel.h"
#include "host/commands/secure_env/in_process_tpm.h"
#include "host/commands/secure_env/keymaster_responder.h"
#include "host/commands/secure_env/tpm_keymaster_context.h"
#include "host/commands/secure_env/tpm_resource_manager.h"
#include "host/libs/config/logging.h"

// Copied from AndroidKeymaster4Device
constexpr size_t kOperationTableSize = 16;

DEFINE_int32(keymaster_fd, -1, "A file descriptor for keymaster communication");

DEFINE_string(keymaster_impl,
              "software",
              "The keymaster implementation. "
              "\"in_process_tpm\" or \"software\"");

int main(int argc, char** argv) {
  cuttlefish::DefaultSubprocessLogging(argv);
  gflags::ParseCommandLineFlags(&argc, &argv, true);
  // keymaster::AndroidKeymaster puts the given pointer into a UniquePtr,
  // taking ownership.
  keymaster::KeymasterContext* keymaster_context;

  std::unique_ptr<InProcessTpm> in_process_tpm;
  std::unique_ptr<ESYS_CONTEXT, void(*)(ESYS_CONTEXT*)> esys(
      nullptr, [](ESYS_CONTEXT* esys) { Esys_Finalize(&esys); });
  std::unique_ptr<TpmResourceManager> resource_manager;

  if (FLAGS_keymaster_impl == "software") {
    keymaster_context =
        new keymaster::PureSoftKeymasterContext(KM_SECURITY_LEVEL_SOFTWARE);
  } else if (FLAGS_keymaster_impl == "in_process_tpm") {
    in_process_tpm.reset(new InProcessTpm());
    ESYS_CONTEXT* esys_ptr = nullptr;
    auto rc =
        Esys_Initialize(&esys_ptr, in_process_tpm->TctiContext(), nullptr);
    if (rc != TPM2_RC_SUCCESS) {
      LOG(FATAL) << "Could not initialize esys: " << Tss2_RC_Decode(rc)
                 << " (" << rc << ")";
    }
    esys.reset(esys_ptr);
    rc = Esys_Startup(esys.get(), TPM2_SU_CLEAR);
    if (rc != TPM2_RC_SUCCESS) {
      LOG(FATAL) << "TPM2_Startup failed: " << Tss2_RC_Decode(rc)
                 << " (" << rc << ")";
    }
    // TODO(schuffelen): Call this only on first boot.
    rc = Esys_Clear(
        esys.get(),
        ESYS_TR_RH_PLATFORM,
        ESYS_TR_PASSWORD,
        ESYS_TR_NONE,
        ESYS_TR_NONE);
    if (rc != TPM2_RC_SUCCESS) {
      LOG(FATAL) << "TPM2_Clear failed: " << Tss2_RC_Decode(rc)
                 << " (" << rc << ")";
    }
    resource_manager.reset(new TpmResourceManager(esys.get()));
    keymaster_context = new TpmKeymasterContext(resource_manager.get());
  } else {
    LOG(FATAL) << "Unknown keymaster implementation " << FLAGS_keymaster_impl;
    return -1;
  }
  keymaster::AndroidKeymaster keymaster{
      keymaster_context, kOperationTableSize};

  CHECK(FLAGS_keymaster_fd != -1)
      << "TODO(schuffelen): Add keymaster_fd alternative";
  auto server = cuttlefish::SharedFD::Dup(FLAGS_keymaster_fd);
  CHECK(server->IsOpen()) << "Could not dup server fd: " << server->StrError();
  close(FLAGS_keymaster_fd);
  auto conn = cuttlefish::SharedFD::Accept(*server);
  CHECK(conn->IsOpen()) << "Unable to open connection: " << conn->StrError();
  cuttlefish::KeymasterChannel keymaster_channel(conn);

  KeymasterResponder keymaster_responder(&keymaster_channel, &keymaster);

  // TODO(schuffelen): Do this in a thread when adding other HALs
  while (keymaster_responder.ProcessMessage()) {
  }
}
