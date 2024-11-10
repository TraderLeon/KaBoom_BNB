const { Telegraf, Markup } = require('telegraf');
const dbClient = require('../config/database');
const { formatSingleTokenMessage, groupBy } = require('./dex_notifications');
const { t } = require('../config/translations');

// Helper function to calculate minutes since insert_time in UTC
// Helper function to calculate minutes since insert_time in UTC
function calculateMinutesSinceInsert(insertTime) {
    const nowUtc = new Date();  // Get current UTC time
    const insertTimestamp = new Date(insertTime + 'Z');  // Ensure insertTime is in UTC

    // Log the current UTC time and insert time
    console.log("Current UTC Time:", nowUtc.toISOString());
    console.log("Insert Time in UTC:", insertTimestamp.toISOString());

    // Calculate the time difference in minutes
    return Math.floor((nowUtc - insertTimestamp) / (1000 * 60)); // Convert milliseconds to minutes
}
// Function to handle "latest_dex_rank" action with language support
async function handleLatestDexRank(ctx) {
    try {
        // Fetch user's language preference
        const userLangRes = await dbClient.query(`SELECT language FROM users WHERE telegram_id = $1`, [ctx.from.id]);
        const userLang = userLangRes.rows.length > 0 ? userLangRes.rows[0].language : 'en';

        await ctx.reply(t(userLang, 'loading_latest_dex_rank'));

        // Fetch the latest DEX data with the required conditions
        const latestDexData = await getLatestDexData();

        // Filter tokens for specific chains
        const filteredDexData = latestDexData.filter(token =>
            ['solana', 'bsc', 'base', 'ethereum'].includes(token.chain_id.toLowerCase()) &&
            token.price_change_h1 > 0
        );

        if (filteredDexData.length === 0) {
            await ctx.reply(t(userLang, 'no_dex_data_available'));
            return;
        }

        // Group tokens by chain_id and group_id for top-ranked selection
        const tokensByChainAndGroup = groupBy(filteredDexData, (token) => `${token.chain_id}_${token.group_id}`);
        
        for (const groupKey in tokensByChainAndGroup) {
            const topTokens = tokensByChainAndGroup[groupKey]
                .filter(token => token.rank <= 5)
                .sort((a, b) => a.rank - b.rank);

            for (const token of topTokens) {
                const message = formatSingleTokenMessage(token, userLang);

                // Calculate minutes since insert time
                const minutesSinceInsert = calculateMinutesSinceInsert(token.insert_time);
                const messageWithInsertTime = `${message}\n⏱️ ${minutesSinceInsert} ${t(userLang, 'minutes_ago')} ${t(userLang, 'signal')}. `;

                await ctx.replyWithHTML(messageWithInsertTime, {
                    disable_web_page_preview: true,
                    ...Markup.inlineKeyboard([
                        Markup.button.url(t(userLang, 'trading_meme'), "https://t.me/kaboom_auth_bot/kaboom")
                    ])
                });
            }
        }

        // Calculate the time to wait for the next update
        const nextUpdateMinutes = calculateMinutesUntilNextUpdate(latestDexData);

        // Inform user about the next update with a "Back" button
        await ctx.reply(`${t(userLang, 'wait_for_next_update')} ${nextUpdateMinutes} ${t(userLang, 'minutes')}:`, {
            reply_markup: {
                inline_keyboard: [
                    [{ text: t(userLang, 'back'), callback_data: 'back_to_start' }]
                ]
            }
        });
    } catch (error) {
        console.error('Error fetching latest DEX data:', error);
        await ctx.reply(t(userLang, 'failed_to_fetch_dex_data'));
    }
}

// Function to get the latest DEX data grouped by chain_id and rank
async function getLatestDexData() {
    const query = `
        SELECT *
        FROM (
            SELECT *, ROW_NUMBER() OVER (PARTITION BY chain_id ORDER BY sent_time DESC) AS row_num
            FROM token_data
            WHERE insert_time >= (NOW() AT TIME ZONE 'UTC') - INTERVAL '60 minutes'
            AND price_change_h1 > 0
            AND chain_id IN ('solana', 'bsc', 'base', 'ethereum')
        ) ranked_tokens
        WHERE row_num <= 3  -- Get up to the latest 3 records per chain_id
        ORDER BY chain_id, rank;
    `;

    const res = await dbClient.query(query);
    return res.rows;
}

// Function to calculate minutes until the next rank update (every 20 minutes)
function calculateMinutesUntilNextUpdate(latestDexData) {
    const latestTimestamp = new Date(latestDexData[0].sent_time);
    const now = new Date();

    // Calculate the time difference in minutes
    const timeElapsed = Math.floor((now - latestTimestamp) / (1000 * 60));
    const timeUntilNextUpdate = 10 - (timeElapsed % 10);

    return timeUntilNextUpdate;
}

module.exports = { handleLatestDexRank };