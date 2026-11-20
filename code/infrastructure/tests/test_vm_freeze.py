"""Exercise VM freeze/recovery without invoking Vagrant or changing real VMs."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
UUID = '12345678-1234-1234-1234-123456789abc'


class WorkerFreezeTests(unittest.TestCase):
    def make_lab(self, folder):
        root = Path(folder)
        (root/'scripts/vm').mkdir(parents=True)
        shutil.copy(ROOT/'scripts/vm/main.sh', root/'scripts/vm/main.sh')
        identity = root/'local/.vagrant/machines/aat-worker-1/libvirt/id'
        identity.parent.mkdir(parents=True)
        identity.write_text(UUID)
        (root/'bin').mkdir()
        (root/'state').write_text('running')
        (root/'bin/virsh').write_text('''#!/bin/sh
shift 2
printf '%s\\n' "$*" >> "$LAB/calls"
case "$1" in
  domname) echo "${DOMAIN_NAME:-aat-m2aat-worker-1}" ;;
  domstate) cat "$LAB/state" ;;
  suspend) echo paused > "$LAB/state" ;;
  resume) echo running > "$LAB/state" ;;
  *) exit 99 ;;
esac
''')
        (root/'bin/vagrant').write_text('#!/bin/sh\nexit 99\n')
        for tool in (root/'bin').iterdir(): tool.chmod(0o755)
        return root, dict(os.environ, LAB=str(root), PATH=str(root/'bin')+os.pathsep+os.environ['PATH'])

    def action(self, root, env, action):
        return subprocess.run(['bash','scripts/vm/main.sh',action,'aat-worker-1'],cwd=root,env=env,
                              capture_output=True,text=True)

    def test_suspend_resume_are_idempotent_and_never_provision(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            for action, state in [('suspend-worker','paused'),('suspend-worker','paused'),
                                  ('resume-worker','running'),('resume-worker','running')]:
                result=self.action(root,env,action)
                self.assertEqual(result.returncode,0,result.stderr)
                self.assertEqual((root/'state').read_text().strip(),state)
            calls=(root/'calls').read_text().splitlines()
            self.assertEqual(calls.count('suspend '+UUID),1)
            self.assertEqual(calls.count('resume '+UUID),1)

    def test_wrong_domain_and_stopped_worker_are_not_modified(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            result=self.action(root,dict(env,DOMAIN_NAME='another-vm'),'suspend-worker')
            self.assertNotEqual(result.returncode,0)
            self.assertEqual((root/'state').read_text().strip(),'running')
            (root/'state').write_text('shut off')
            self.assertNotEqual(self.action(root,env,'resume-worker').returncode,0)
            self.assertEqual((root/'state').read_text(),'shut off')

if __name__=='__main__': unittest.main()
