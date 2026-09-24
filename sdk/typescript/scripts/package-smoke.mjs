import {execFileSync} from 'node:child_process';
import {mkdtempSync, writeFileSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
const directory = mkdtempSync(join(tmpdir(), 'yukkuri-package-'));
const npm = (args, cwd) => execFileSync(process.execPath, [process.env.npm_execpath, ...args], {cwd, encoding:'utf8', stdio:['ignore','pipe','pipe']});
try {
  npm(['pack','--pack-destination',directory], process.cwd());
  writeFileSync(join(directory,'package.json'), JSON.stringify({private:true,type:'module'}));
  npm(['install','--offline','--ignore-scripts',join(directory,'yukkuri-realtime-client-0.1.0.tgz')],directory);
  writeFileSync(join(directory,'smoke.mjs'), `import {YukkuriClient, PROTOCOL_VERSION} from '@yukkuri-realtime/client';
import {BrowserAudioPlayer} from '@yukkuri-realtime/client/browser';
import {readFileSync} from 'node:fs';
if (PROTOCOL_VERSION !== '1' || !new YukkuriClient() || !BrowserAudioPlayer) throw Error('exports');
if (!readFileSync(new URL(import.meta.resolve('@yukkuri-realtime/client/audio-worklet.js')), 'utf8').includes('registerProcessor')) throw Error('asset');
console.log('Installed package core/browser/Worklet exports passed');`);
  process.stdout.write(execFileSync(process.execPath, ['smoke.mjs'], {cwd:directory,encoding:'utf8'}));
} finally { rmSync(directory,{recursive:true,force:true}); }
