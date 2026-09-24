import {writeFile} from 'node:fs/promises';
import {YukkuriClient} from '../../../sdk/typescript/dist/index.js';
const client = new YukkuriClient({baseUrl: process.env.YUKKURI_ENGINE_URL});
const result = await client.speak('ゆっくりしていってね');
await writeFile('hello.wav', result.audio);
console.log(result.format, result.requestId);
