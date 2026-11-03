"""Failure contracts for the E2E runner; fake CLIs never touch a cluster."""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
KUBECTL = shutil.which("kubectl")
SKAFFOLD = shutil.which("skaffold")
FAKE = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
name=pathlib.Path(sys.argv[0]).name
args=sys.argv[1:]
fault=os.environ.get('FAULT','')
with open(os.environ['TRACE'],'a') as f: f.write(json.dumps([name,args])+'\n')
def stop(code): sys.exit(code)
if name=='kind':
    if fault=='missing-kind': stop(3)
    print('local-kind-credentials')
elif name=='kubectl':
    config=pathlib.Path(os.environ['KUBECONFIG'])
    assert config.stat().st_mode & 0o777 == 0o600
    pathlib.Path(os.environ['SCOPED']).write_text(str(config))
    if 'get' in args and 'namespace' in args:
        if fault=='unreachable': stop(3)
        if fault=='existing': print('namespace/aqsh')
    elif 'create' in args:
        if fault=='namespace-race': stop(4)
    elif 'get' in args and 'pods' in args and '-o' in args:
        if fault!='no-pods': print('aqsh-pod')
    elif 'exec' in args:
        if fault in ('flush','verify-and-flush'): stop(9)
    elif 'cp' in args:
        if fault=='copy': stop(9)
        if fault!='empty-coverage':
            p=pathlib.Path(args[-1])
            if fault!='counters-only': (p/'covmeta.test').touch()
            if fault!='meta-only': (p/'covcounters.test').touch()
elif name=='skaffold':
    stage=args[0]
    if fault==stage or (stage=='verify' and fault in ('verify-and-flush','verify-and-delete')) or (stage=='delete' and fault=='verify-and-delete'): stop(17)
elif name=='go':
    if fault=='covdata': stop(13)
'''


class RunnerContracts(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name)
        # Run the actual runner in an isolated project so coverage never touches
        # existing developer artifacts, even when a test fails.
        (self.path / 'test').mkdir()
        shutil.copy(ROOT / 'test/e2e.sh', self.path / 'test/e2e.sh')
        self.bin = self.path / 'bin'
        self.bin.mkdir()
        for name in ['kind', 'kubectl', 'skaffold', 'go']:
            p = self.bin / name
            p.write_text(FAKE)
            p.chmod(0o755)
        self.trace = self.path / 'trace'
        self.original = self.path / 'user-kubeconfig'
        self.original.write_text('do not modify')

    def run_case(self, *args, fault=''):
        env = dict(os.environ, PATH=str(self.bin)+os.pathsep+os.environ['PATH'],
                   KUBECONFIG=str(self.original), TRACE=str(self.trace), FAULT=fault,
                   SCOPED=str(self.path/'scoped-config-path'))
        result = subprocess.run(['bash', str(self.path/'test/e2e.sh'), *args],
                                env=env, capture_output=True, text=True, timeout=10)
        self.calls = [json.loads(x) for x in self.trace.read_text().splitlines()] if self.trace.exists() else []
        self.assertEqual(self.original.read_text(), 'do not modify')
        scoped=self.path/'scoped-config-path'
        if scoped.exists():
            self.assertFalse(Path(scoped.read_text()).exists(), 'temporary credentials were not removed')
        return result

    def deleted(self):
        return any(name=='skaffold' and args[0]=='delete' for name,args in self.calls)

    def test_reject_unsafe_arguments_before_any_command(self):
        for args in [[], [''], ['kind-'], ['kind-test','--profile'],
                     ['kind-test','--profile','--namespace=other'], ['production'], ['kind-test', '--namespace', 'other'],
                     ['kind-test', '--kube-context', 'other'], ['kind-test', '--profile', 'remote'],
                     ['kind-test', '--coverage', '--profile', 'native-auth']]:
            with self.subTest(args=args):
                result=self.run_case(*args)
                self.assertEqual(result.returncode, 2)
                self.assertEqual(self.calls, [])

    def test_never_delete_resources_without_namespace_claim(self):
        for fault in ['missing-kind', 'existing', 'unreachable', 'build', 'namespace-race']:
            with self.subTest(fault=fault):
                self.trace.unlink(missing_ok=True)
                result=self.run_case('kind-test', fault=fault)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.deleted())
                self.assertFalse(any(n=='kubectl' and 'delete' in a for n,a in self.calls))

    def test_deploy_and_verify_failures_cleanup_and_preserve_status(self):
        for fault in ['deploy', 'verify', 'verify-and-flush', 'verify-and-delete']:
            with self.subTest(fault=fault):
                self.trace.unlink(missing_ok=True)
                args=['kind-test']+(['--coverage'] if fault=='verify-and-flush' else [])
                self.assertEqual(self.run_case(*args, fault=fault).returncode, 17)
                self.assertTrue(self.deleted())

    def test_cleanup_failure_is_failure(self):
        self.assertNotEqual(self.run_case('kind-test', fault='delete').returncode, 0)

    def test_coverage_failures_are_not_ignored(self):
        for fault in ['no-pods', 'flush', 'copy', 'empty-coverage', 'meta-only', 'counters-only', 'covdata']:
            with self.subTest(fault=fault):
                self.trace.unlink(missing_ok=True)
                self.assertNotEqual(self.run_case('kind-test', '--coverage', fault=fault).returncode, 0)
                self.assertTrue(self.deleted())

    def test_one_build_and_scoped_commands_with_fresh_coverage(self):
        result=self.run_case('kind-test', '--coverage')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sum(n=='skaffold' and a[0]=='build' for n,a in self.calls), 1)
        for name,args in self.calls:
            if name=='kubectl':
                self.assertEqual(args[args.index('--context')+1], 'kind-test')
                self.assertEqual(args[args.index('--namespace')+1], 'aqsh')
        self.assertTrue(self.deleted())
        delete_resources=next(i for i,(n,a) in enumerate(self.calls) if n=='skaffold' and a[0]=='delete')
        delete_namespace=next(i for i,(n,a) in enumerate(self.calls) if n=='kubectl' and 'delete' in a)
        self.assertLess(delete_resources,delete_namespace)
        self.assertEqual(len(list((self.path/'coverage').glob('e2e.*'))), 1)


class DeploymentContracts(unittest.TestCase):
    def test_example_and_test_overlay_are_separate_and_namespaces_match(self):
        self.assertIsNotNone(KUBECTL, 'kubectl is required for offline manifest checks')
        for path,fixtures in [('k8s',False),('test/fixtures',True)]:
            with self.subTest(path=path):
                output=subprocess.check_output([KUBECTL,'--context','render-only',
                    '--namespace','aqsh','kustomize',path],cwd=ROOT,text=True)
                self.assertEqual('name: mock-values\n' in output, fixtures)
                self.assertEqual('user-restricted:' in output, fixtures)
                self.assertIn('name: aqsh-tasks-config', output)
                self.assertIn('name: aqsh-task-scripts', output)
                self.assertTrue(re.findall(r'^  namespace: (\S+)$',output,re.M))
                self.assertEqual(set(re.findall(r'^  namespace: (\S+)$',output,re.M)), {'aqsh'})
                self.assertEqual('kind: Namespace\n' in output, not fixtures)
        self.assertIsNotNone(SKAFFOLD, 'skaffold is required for offline profile checks')
        for profiles,fixtures in [([],False),(['--profile','e2e'],True)]:
            output=subprocess.check_output([SKAFFOLD,'render','--offline',
                '--digest-source=none','--namespace','aqsh',*profiles],cwd=ROOT,text=True)
            self.assertEqual('name: mock-values\n' in output, fixtures)
            self.assertEqual('user-restricted:' in output, fixtures)
        make=(ROOT/'Makefile').read_text()
        self.assertIn('./test/e2e.sh "$(KUBE_CONTEXT)" $(E2E_ARGS)',make)
        self.assertFalse((ROOT/'scripts/run-e2e-test.sh').exists())


if __name__=='__main__':
    unittest.main(verbosity=2)
