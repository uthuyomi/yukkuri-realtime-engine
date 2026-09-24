import {YukkuriClient} from '../../../sdk/typescript/dist/index.js';
const client = new YukkuriClient({baseUrl: process.env.YUKKURI_ENGINE_URL});
const session = await client.realtime.connect();
session.on('textDelta', e => process.stdout.write(e.delta));
session.on('error', e => console.error(e.code, e.message));
try { await (await session.sendText(process.argv[2] ?? '札幌について教えて')).done; }
finally { await session.close(); }
console.log();
