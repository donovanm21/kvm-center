"""Regression for daemon-owned resources surviving a timed-out test process."""
import importlib.util
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
spec=importlib.util.spec_from_file_location('distributable', Path(__file__).resolve().parents[1]/'validate-distributable.py')
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class CleanupTests(unittest.TestCase):
    def test_timeout_does_not_skip_other_owned_resources(self):
        ok=subprocess.CompletedProcess([],0,b'',b'')
        with patch.object(module.subprocess,'run',side_effect=[subprocess.TimeoutExpired('docker',30),ok,ok,ok]) as run:
            self.assertFalse(module.cleanup_owned('runtime','volume','extract','image',True,True,True))
        self.assertEqual([c.args[0] for c in run.call_args_list], [
            ['docker','rm','-f','runtime'],['docker','volume','rm','volume'],
            ['docker','rm','-f','extract'],['docker','image','rm','-f','image']])
        self.assertTrue(all(c.kwargs['timeout']==30 for c in run.call_args_list))

    def test_already_removed_is_success_but_daemon_error_is_not(self):
        for message,expected in [(b'No such container: runtime',True),(b'Cannot connect to Docker daemon',False)]:
            with patch.object(module.subprocess,'run',return_value=subprocess.CompletedProcess([],1,b'',message)):
                self.assertEqual(module.cleanup_owned('runtime','volume','extract','image',True,False,False),expected)

if __name__=='__main__': unittest.main()
