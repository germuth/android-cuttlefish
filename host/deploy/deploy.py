"""Fetch and Deploy Cuttlefish images to specified GCE instance.
"""

import argparse
import logging
import os
import sys
import tempfile

LOG_FORMAT = "%(levelname)1.1s %(asctime)s %(process)d %(filename)s:%(lineno)d] %(message)s"
LOG = logging.getLogger()
SSH_ARGS = ' '.join([
    '-o', 'StrictHostKeyChecking=no',
    '-o', 'UserKnownHostsFile=/dev/null',
    '-q',
])

def setup_arg_parser():
    """Set up command line argument parser."""
    parser = argparse.ArgumentParser()
    parser.add_argument('--system_build', type=str, required=False,
                        default='latest',
                        help='Build number to fetch from Android Build server.')
    parser.add_argument('--system_branch', type=str, required=False,
                        default='git_oc-gce-dev',
                        help='Android Build branch providing system images.')
    parser.add_argument('--system_target', type=str, required=False,
                        default='cf_x86_phone-userdebug',
                        help='Android Build target providing system images.')
    parser.add_argument('--kernel_branch', type=str, required=False,
                        default='kernel-n-dev-android-gce-3.18-x86_64',
                        help='Android Build branch providing kernel images.')
    parser.add_argument('--kernel_target', type=str, required=False,
                        default='kernel',
                        help='Android Build target providing kernel images.')
    parser.add_argument('-i', '--instance', type=str, required=True,
                        help='IP address of GCE instance.')
    parser.add_argument('--tmpdir', type=str, required=False,
                        default='/tmp',
                        help='Temporary folder location.')
    parser.add_argument('--keep', action='store_true',
                        help='Keep downloaded archive file after completion (=do not clean up).')
    parser.add_argument('--instance_folder', type=str, required=False,
                        default='/srv/cf',
                        help='Folder on the remote machine where images should be deployed.')
    return parser


def setup_logger():
    """Set up logging mechanism.

    Logging mechanism will print logs out to standard error.
    """
    stdout_handler = logging.StreamHandler(sys.stderr)
    logging.basicConfig(
        level=logging.DEBUG,
        format=LOG_FORMAT,
        handlers=[stdout_handler])


def execute_remote(server, command):
    """Execute supplied command on a remote server."""
    LOG.info('Executing remote command: %s', command)
    cmd = os.popen('ssh ${USER}@%s %s -- %s' % (server, SSH_ARGS, command))
    cmd_out = [line for line in cmd.xreadlines()]
    if cmd.close():
        raise Exception('Could not execute: %s:\n\t%s',
                        command,
                        '\n\t'.join(cmd_out))


def execute(command):
    """Execute supplied command. Raise exception if execution failed."""
    cmd = os.popen(command)
    LOG.info('Executing: %s', command)
    cmd_out = [line for line in cmd.xreadlines()]
    if cmd.close():
        raise Exception('Could not execute: %s:\n\t%s',
                        command,
                        '\n\t'.join(cmd_out))



def main():
    """Deploy Cuttlefish image to GCE."""
    setup_logger()
    parser = setup_arg_parser()
    args = parser.parse_args()

    # Do all work in temp folder.
    # Allow system to fail here with an exception.
    os.chdir(args.tmpdir)
    target_dir = '%s/%s' % (args.instance_folder, args.system_build)
    temp_image = '%s-%s.img' % (args.system_target, args.system_build)

    try:
        build_selector = '--latest'
        if args.system_build != 'latest':
            build_selector = '--bid=' + args.system_build

        if not os.path.exists(temp_image):
            execute('/google/data/ro/projects/android/fetch_artifact '
                    '%s --branch=%s --target=%s '
                    '\'cf_x86_phone-img-*\' \'%s\'' %
                    (build_selector, args.system_branch, args.system_target, temp_image))

        execute('/google/data/ro/projects/android/fetch_artifact '
                '--latest --branch=%s --target=%s '
                'bzImage kernel' %
                (args.kernel_branch, args.kernel_target))
        execute('unzip -u \'%s\' system.img boot.img' % temp_image)

        execute_remote(args.instance, 'sudo mkdir -p %s' % target_dir)

        # Give user and libvirt access rights to specified folder.
        # Remote directory appears as 'no access rights' except for included
        # users.
        execute_remote(args.instance, 'sudo chmod ugo= %s' % target_dir)
        execute_remote(args.instance, 'sudo setfacl -m u:${USER}:rwx %s' % target_dir)
        execute_remote(args.instance, 'sudo setfacl -m u:libvirt-qemu:rwx %s' % target_dir)

        LOG.info('Ensuring user is a member of relevant groups.')
        # TODO(ender): This technically does not belong in here.
        execute_remote(args.instance, 'sudo usermod -a -G libvirt ${USER}')

        # Copy files to remote server location.
        execute('scp %s kernel system.img boot.img ${USER}@%s:%s' %
                (SSH_ARGS, args.instance, target_dir))
        execute_remote(args.instance, 'setfacl -m u:${USER}:rw %s/*' % target_dir)
        execute_remote(args.instance, 'setfacl -m u:libvirt-qemu:rw %s/*' % target_dir)

    except Exception as exception:
        LOG.exception('Could not complete: %s', exception)
    finally:
        if not args.keep:
            os.unlink(temp_image)
        os.unlink('boot.img')
        os.unlink('kernel')
        os.unlink('system.img')


if __name__ == '__main__':
    main()
