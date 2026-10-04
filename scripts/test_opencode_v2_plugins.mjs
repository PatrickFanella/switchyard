import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, statSync, symlinkSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { persistCredential } from '../services/opencode/default-config/plugins/managed-credentials.mjs';
import rtk from '../services/opencode/default-config/plugins/rtk.ts';
import managed from '../services/opencode/default-config/plugins/switchyard-blacktower.ts';

test('credential refresh survives an in-memory database restart without exposing permission state', () => {
 const directory=mkdtempSync(join(tmpdir(),'switchyard-v2-auth-'));mkdirSync(join(directory,'opencode'));
 try {
  persistCredential('fixture',{type:'oauth',refresh:'old',access:'old-access',expires:1},directory);
  persistCredential('other',{type:'key',key:'retained'},directory);
  persistCredential('fixture',{type:'oauth',refresh:'new',access:'new-access',expires:2,metadata:{accountID:'account',untrusted:'discard'}},directory);
  const target=join(directory,'opencode','auth.json');const data=JSON.parse(readFileSync(target,'utf8'));
  assert.deepEqual(data.fixture,{type:'oauth',refresh:'new',access:'new-access',expires:2,accountId:'account'});
  assert.deepEqual(data.other,{type:'api',key:'retained'});assert.equal(statSync(target).mode&0o777,0o600);
 } finally {rmSync(directory,{recursive:true,force:true});}
});
test('credential mirror refuses a symlink', () => {
 const directory=mkdtempSync(join(tmpdir(),'switchyard-v2-auth-'));mkdirSync(join(directory,'opencode'));
 try { symlinkSync('/etc/passwd',join(directory,'opencode','auth.json'));assert.throws(()=>persistCredential('fixture',{type:'key',key:'fixture'},directory),/regular file/); }
 finally {rmSync(directory,{recursive:true,force:true});}
});
test('RTK uses the native shell hook without double-prefixing commands', async () => {
 let callback;await rtk.setup({shell:{hook:async(name,fn)=>{assert.equal(name,'create.before');callback=fn;}}});
 const event={command:'git status'};callback(event);assert.equal(event.command,'rtk git status');callback(event);assert.equal(event.command,'rtk git status');
});
test('managed metadata uses native request options', async () => {
 let callback;await managed.setup({session:{hook:async(name,fn)=>{assert.equal(name,'context');callback=fn;}},provider:{list:async()=>({data:[]})}});
 const event={model:{providerID:'fixture'},options:{metadata:{retained:true}}};await callback(event);assert.equal(event.options.metadata.switchyard_managed,true);assert.equal(event.options.metadata.retained,true);
});
