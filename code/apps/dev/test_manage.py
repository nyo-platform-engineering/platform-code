import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

import manage


class StartupTests(unittest.TestCase):
    def test_broken_launcher_falls_back_in_project_context(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            for name in ('broken', 'working'):
                launcher = root / name / 'pnpm'
                launcher.parent.mkdir()
                launcher.write_text('#!/bin/sh\n')
                launcher.chmod(0o755)
            expected = '12.6.0'
            results = [subprocess.CompletedProcess([], 1, '', 'MODULE_NOT_FOUND'),
                       subprocess.CompletedProcess([], 0, expected + '\n', '')]
            with patch.object(manage.os, 'get_exec_path', return_value=[str(root / 'broken'), str(root / 'working')]), \
                    patch.object(manage.subprocess, 'run', side_effect=results) as run:
                self.assertEqual(manage.resolve_pnpm(), str(root / 'working/pnpm'))
                self.assertEqual(run.call_count, 2)
                self.assertTrue(all(call.kwargs['cwd'] == manage.FRONTEND for call in run.call_args_list))

    def test_exited_process_fails_before_http_probe(self):
        process = Mock(returncode=1)
        process.poll.return_value = 1
        with patch.object(manage, 'request') as request:
            with self.assertRaisesRegex(RuntimeError, 'exited with code 1.*frontend.log'):
                manage.wait_for('http://127.0.0.1:5173/', process=process, logfile='frontend.log')
            request.assert_not_called()

    def test_live_process_can_become_ready(self):
        process = Mock()
        process.poll.return_value = None
        with patch.object(manage, 'request', return_value=(200, b'ok')):
            manage.wait_for('http://127.0.0.1:5173/', process=process)


if __name__ == '__main__':
    unittest.main()
