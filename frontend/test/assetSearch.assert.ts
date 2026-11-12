import {assetMatchesQuery} from '../src/assets/assetSearch';

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// Matching is a case-insensitive substring over the id and the alias.
assert(assetMatchesQuery({id: 'vid_ab12cd34ef56'}, ''), 'an empty query matches everything');
assert(assetMatchesQuery({id: 'vid_ab12cd34ef56'}, '   '), 'a whitespace query matches everything');
assert(assetMatchesQuery({id: 'vid_ab12cd34ef56'}, 'vid_ab12cd34ef56'), 'the full id matches');
assert(assetMatchesQuery({id: 'vid_ab12cd34ef56'}, 'VID_AB12'), 'an id prefix matches case-insensitively');
assert(assetMatchesQuery({id: 'vid_ab12cd34ef56'}, '34ef'), 'a mid-id fragment matches');
assert(!assetMatchesQuery({id: 'vid_ab12cd34ef56'}, 'img_'), 'a non-matching query misses');
assert(assetMatchesQuery({id: 'vid_ab12cd34ef56', alias: 'Hero Shot'}, 'hero'), 'an alias matches case-insensitively');
assert(assetMatchesQuery({id: 'vid_ab12cd34ef56', alias: 'Hero Shot'}, 'RO SH'), 'a mid-alias fragment matches');
assert(!assetMatchesQuery({id: 'vid_ab12cd34ef56', alias: 'Hero Shot'}, 'vid_ab12cd34ef56 hero nope'), 'no cross-token AND');
assert(assetMatchesQuery({id: 'vid_ab12cd34ef56', alias: 'Hero Shot'}, 'vid'), 'id and alias are ORed');
assert(!assetMatchesQuery({id: 'vid_ab12cd34ef56'}, 'hero'), 'an asset without an alias matches by id only');

console.log('asset search tests passed');
