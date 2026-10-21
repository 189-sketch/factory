import copy
import importlib.util
import json
import subprocess
import sqlite3
import contextlib
import io
import tempfile
import textwrap
import os
import unittest
from unittest.mock import patch
from pathlib import Path

spec=importlib.util.spec_from_file_location('gke_deploy',Path(__file__).with_name('deploy.py'))
deploy=importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)
IMAGE='registry/image@sha256:'+'a'*64
STATE={'database':'jobs.db','jobs':{'job1':{'state':'succeeded','hold':True}},'automations':{'auto1':{'paused':True,'scheduled':True}},'repositories':['owner/repo'],'connected':True}


class FakeDeployment(deploy.Deployment):
    def __init__(self, fail=None):
        super().__init__('test'); self.calls=[]; self.fail=fail
    def status(self):return copy.deepcopy(STATE)
    def get(self,*args,**kwargs):return {'spec':{'replicas':1,'strategy':{'type':'Recreate'},'template':{'spec':{'containers':[{'image':IMAGE}]}}}}
    def no_runners(self):self.calls.append(('no_runners',))
    def k(self,*args,**kwargs):
        self.calls.append(args)
        if self.fail and self.fail(args,kwargs):raise RuntimeError('injected failure')
        return ''


class DeploymentTests(unittest.TestCase):
    def test_active_work_prevents_any_mutation(self):
        for state in ['running','queued','leased','cancelling','awaiting_merge']:
            fake=FakeDeployment(); active=copy.deepcopy(STATE); active['jobs']['job1']['state']=state
            fake.status=lambda:active
            with self.assertRaises(RuntimeError):fake.run(IMAGE,IMAGE,'a'*40,'unused')
            self.assertEqual(fake.calls,[])

    def test_running_schedules_must_be_paused(self):
        s=copy.deepcopy(STATE);s['automations']['auto1']['paused']=False
        with self.assertRaises(RuntimeError):deploy.require_idle(s)

    def test_history_holds_and_paused_schedules_must_survive(self):
        for change in ['history','hold','pause','project']:
            after=copy.deepcopy(STATE)
            if change=='history':after['jobs']={}
            if change=='hold':after['jobs']['job1']['hold']=False
            if change=='pause':after['automations']['auto1']['paused']=False
            if change=='project':after['repositories']=[]
            with self.assertRaises(RuntimeError):deploy.preserved(STATE,after)

    def test_snapshot_failure_never_changes_images_or_restarts_owner(self):
        fake=FakeDeployment(lambda args,kw:args[0]=='wait' and any('volumesnapshot/' in a for a in args))
        with self.assertRaises(RuntimeError):fake.run(IMAGE,IMAGE,'a'*40,'unused')
        self.assertTrue(any(c[:2]==('delete','pod') for c in fake.calls))
        self.assertFalse(any(c[:2]==('set','image') for c in fake.calls))
        self.assertFalse(any('--replicas=1' in c for c in fake.calls))
        self.assertFalse(any(deploy.CONFIG in c for c in fake.calls))

    def test_backup_completes_before_configuration_and_rollout(self):
        fake=FakeDeployment()
        with tempfile.TemporaryDirectory() as temp:
            output=Path(temp)/'release.json'
            with contextlib.redirect_stdout(io.StringIO()):fake.run(IMAGE,IMAGE,'a'*40,output)
            self.assertEqual(json.loads(output.read_text())['jobs_preserved'],1)
        calls=fake.calls
        backup=next(i for i,c in enumerate(calls) if deploy.BACKUP in c)
        ready=next(i for i,c in enumerate(calls) if any('volumesnapshot/' in a for a in c))
        config=next(i for i,c in enumerate(calls) if deploy.CONFIG in c)
        delete=next(i for i,c in enumerate(calls) if c[:2]==('delete','pod'))
        restart=next(i for i,c in enumerate(calls) if '--replicas=1' in c)
        self.assertLess(backup,ready);self.assertLess(ready,config);self.assertLess(config,delete);self.assertLess(delete,restart)

    def test_final_main_gate_skips_stale_revision_and_fails_closed(self):
        workflow=(Path(__file__).parents[2]/'.github/workflows/deploy-gke.yml').read_text()
        step=workflow.split('      - name: Back up, deploy and verify',1)[1]
        script=textwrap.dedent(step.split('        run: |\n',1)[1].split('          python3 deploy/kubernetes/deploy.py',1)[0])
        for current, failed, expected in [('a'*40,False,True),('b'*40,False,False),('',True,False)]:
            gh='gh() { return 1; }\n' if failed else 'gh() { printf %s '+current+'; }\n'
            result=subprocess.run(['bash','-c',gh+script+'printf MUTATED'],env={**os.environ,'REVISION':'a'*40,'GH_REPO':'owainlewis/machinist'},text=True,capture_output=True)
            self.assertEqual('MUTATED' in result.stdout,expected)
            self.assertEqual(result.returncode!=0,failed)

    def test_post_start_failures_stop_unverified_owner(self):
        for phase in ['rollout', 'history', 'check', 'cleanup']:
            fake=FakeDeployment()
            original_k=fake.k
            def k(*args, **kwargs):
                if phase=='rollout' and args[:3]==('rollout','status','deployment/machinist-control-plane'):
                    raise RuntimeError('rollout timed out')
                if phase=='check' and deploy.CHECK in args:
                    raise RuntimeError('sandbox check failed')
                return original_k(*args, **kwargs)
            fake.k=k
            if phase=='history':
                changed=copy.deepcopy(STATE); changed['jobs']={}
                states=iter([copy.deepcopy(STATE), changed]); fake.status=lambda: next(states)
            if phase=='cleanup':
                def no_runners():
                    if any('--replicas=1' in c for c in fake.calls):
                        raise RuntimeError('runner remains')
                fake.no_runners=no_runners
            with tempfile.TemporaryDirectory() as temp:
                output=Path(temp)/'release.json'
                with self.assertRaises(RuntimeError):fake.run(IMAGE,IMAGE,'a'*40,output)
                self.assertFalse(output.exists())
            self.assertIn('--replicas=1',next(c for c in fake.calls if '--replicas=1' in c))
            self.assertEqual(fake.calls[-1],('scale','deployment/machinist-control-plane','--replicas=0'))

    def test_operator_snapshot_requires_ready_without_granting_cluster_access(self):
        fake=FakeDeployment();fake.snapshot_backend='gcloud'
        def get(kind,name):
            if kind=='pvc':return {'spec':{'volumeName':'disk-pv'}}
            return {'spec':{'csi':{'driver':'pd.csi.storage.gke.io','volumeHandle':'projects/test/zones/europe-west2-b/disks/state'}}}
        fake.get=get
        with patch.object(deploy.subprocess,'run') as run, patch.object(deploy.subprocess,'check_output',return_value='CREATING'):
            with self.assertRaises(RuntimeError):fake.snapshot('backup')
            self.assertEqual(run.call_args.args[0][:4],['gcloud','compute','disks','snapshot'])
        self.assertEqual(fake.calls,[])

    def test_backup_supports_legacy_and_current_database_without_mutating_source(self):
        for name in ['jobs.db','factory.db']:
            with tempfile.TemporaryDirectory() as temp:
                root=Path(temp)
                with sqlite3.connect(root/name) as db:
                    db.execute('create table jobs(id text)');db.execute("insert into jobs values ('preserved')")
                result=subprocess.run(['python3','-c',deploy.BACKUP.replace('/var/lib/machinist',temp),'release','{}'],capture_output=True,text=True)
                self.assertEqual(result.returncode,0,result.stderr)
                with sqlite3.connect(root/'deployments/release/jobs.db') as backup:
                    self.assertEqual(backup.execute('select id from jobs').fetchone(),('preserved',))
                self.assertEqual([p.name for p in root.glob('*.db')],[name])

    def test_configuration_update_changes_only_the_runner_digest(self):
        with tempfile.TemporaryDirectory() as temp:
            p=Path(temp)/'config.toml'
            before='[server]\nstate = "factory"\n[server.kubernetes]\nimage = "old@sha256:'+ 'b'*64 +'"\nauth_secret = "keep-me"\n[other]\nvalue = "preserve"\n'
            p.write_text(before)
            result=subprocess.run(['python3','-c',deploy.CONFIG.replace('/var/lib/machinist',temp),IMAGE],capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertIn('auth_secret = "keep-me"',p.read_text())
            self.assertIn('[other]\nvalue = "preserve"',p.read_text())
            self.assertIn('image = '+json.dumps(IMAGE),p.read_text())

    def test_tags_and_malformed_revisions_never_reach_cluster(self):
        fake=FakeDeployment()
        for image,revision in [('registry/image:latest','a'*40),(IMAGE,'main')]:
            with self.assertRaises(ValueError):fake.run(image,IMAGE,revision,'unused')
        self.assertEqual(fake.calls,[])

if __name__=='__main__':unittest.main()
