require('dotenv').config({ path: '../.env' });
const { Telegraf, Markup } = require('telegraf');
const dbClient = require('../config/database');
const bot = require('../bot_instance');
const { t } = require('../config/translations');
const { ChartJSNodeCanvas } = require('chartjs-node-canvas');

// Mapping of chain names to IDs
const GOPLUS_CHAIN_ID_MAP = {
    "ethereum": "1",
    "optimism": "10",
    "cronos": "25",
    "bsc": "56",
    "gnosis": "100",
    "heco": "128",
    "polygon": "137",
    "fantom": "250",
    "kcc": "321",
    "zksync_era": "324",
    "ethw": "10001",
    "fon": "201022",
    "arbitrum": "42161",
    "avalanche": "43114",
    "linea": "59144",
    "base": "8453",
    "tron": "tron",
    "scroll": "534352",
    "opbnb": "204",
    "mantle": "5000",
    "zkfair": "42766",
    "blast": "81457",
    "manta_pacific": "169",
    "berachain_artio_testnet": "80085",
    "merlin": "4200",
    "bitlayer_mainnet": "200901",
    "zklink_nova": "810180",
    "x_layer_mainnet": "196",
    "solana": "900"
};

async function sendDexNotifications() {
    try {
        console.log('Starting sendDexNotifications function');

        // Fetch unsent DEX token data
        const tokenDataRes = await dbClient.query(`
            SELECT *
            FROM (
                SELECT *, ROW_NUMBER() OVER (PARTITION BY group_id ORDER BY rank) AS row_num
                FROM token_data
                WHERE sent_time IS NULL 
                  AND insert_time >= (NOW() AT TIME ZONE 'UTC') - INTERVAL '10 minutes'
                  AND chain_id IN ('solana', 'bsc', 'base', 'ethereum')
                  AND price_change_h1 > 5
            ) ranked_tokens
            WHERE row_num <= 5
            ORDER BY group_id, rank;
        `);
        const tokenData = tokenDataRes.rows;

        console.log(`Found ${tokenData.length} unsent DEX token data`);
        if (tokenData.length === 0) return;

        // Fetch active users with subscriptions and their language settings
        const usersRes = await dbClient.query(`
            SELECT id, telegram_id, cex_subscriptions, language
            FROM users 
            WHERE active = true;
        `);
        const users = usersRes.rows;

        // Group users by language
        const usersByLanguage = groupBy(users, (user) => user.language || 'en');
        
        // Prepare and send notifications for each language group
        for (const [lang, userGroup] of Object.entries(usersByLanguage)) {
            const preparedContent = await prepareDexNotificationsContent(tokenData, lang);
            await sendDexNotificationsToUsers(userGroup, preparedContent);
        }
        // Update `sent_time` for all tokens in tokenData after notifications are sent to all language groups
        const tokenIds = tokenData.map(token => token.id);
        await dbClient.query(`
            UPDATE token_data 
            SET sent_time = NOW() AT TIME ZONE 'UTC' 
            WHERE id = ANY($1)
        `, [tokenIds]);

        console.log("sendDexNotifications completed.");
    } catch (error) {
        console.error('Error sending DEX notifications:', error);
    }
}

async function prepareDexNotificationsContent(tokenData, lang) {
    const tokensByChainAndGroup = groupBy(
        tokenData.filter(token => token.rank <= 5),
        (token) => `${token.chain_id}_${token.group_id}`
    );

    const preparedContent = [];
    const chartCache = {}; // Cache charts by token address to avoid duplicate generation

    for (const groupKey in tokensByChainAndGroup) {
        const topTokens = tokensByChainAndGroup[groupKey].sort((a, b) => a.rank - b.rank);

        for (const token of topTokens) {
            console.log(`Preparing content for token: ${token.base_token_symbol} on chain: ${token.chain_id}`);

            // Check if a recent notification was sent for this token
            const oneHourAgo = new Date(Date.now() - 60 * 60 * 1000);
            const recentlySentRes = await dbClient.query(`
                SELECT 1 FROM token_data 
                WHERE base_token_symbol = $1 
                  AND sent_time IS NOT NULL
                  AND sent_time >= $2
            `, [token.base_token_symbol, oneHourAgo]);

            if (recentlySentRes.rows.length > 0) {
                console.log(`Skipping ${token.base_token_symbol} (recently sent)`);
                continue;
            }

            // Calculate the rally since the first recorded entry for this token
            const { firstEntry, isFirstCall } = await getFirstTokenEntry(token.base_token_address);
            let rallyMessage = '';

            if (firstEntry) {
                const initialPrice = parseFloat(firstEntry.price_usd);
                const currentPrice = parseFloat(token.price_usd);
                if (initialPrice > 0 && currentPrice > initialPrice) {
                    const rallyPercent = ((currentPrice - initialPrice) / initialPrice) * 100;
                    
                    rallyMessage = t(lang, 'rally_message')?.replace('{percent}', formatPercent(rallyPercent, false));
            
                    if (!rallyMessage) {
                        console.error(`Translation for 'rally_message' not found for language: ${lang}`);
                    }
            
                    if (isFirstCall) {
                        rallyMessage += "\n❗️First Signal❗️";
                    }
                }
            }

            // Check if the chart for this token address is already cached
            if (!chartCache[token.base_token_address]) {
                // Fetch historical price data for the chart
                const priceData = await fetchPriceHistory(token.chain_id, token.base_token_address, token.launched_days);

                // Fetch notifications for marking events on the chart
                let buySignals = await getTokenNotifications(token.base_token_address);
                
                // Adjust buy signal timestamps to match the nearest previous hour and get prices from priceData
                buySignals = adjustBuySignalTimes(buySignals, priceData);

                // Create the price chart with adjusted buy signal times
                chartCache[token.base_token_address] = await createPriceChart(priceData, token.base_token_symbol, token.base_token_name, buySignals);
            }

            const chartImage = chartCache[token.base_token_address];
            const chainId = GOPLUS_CHAIN_ID_MAP[token.chain_id.toLowerCase()] || "default_chain_id";
            const kaboomUrl = `https://t.me/kaboom_auth_bot/kaboom?startapp=${chainId}-${token.pair_address}`;
            const message = `${formatSingleTokenMessage(token, lang)}\n${rallyMessage}`;
            console.log(`Final message for ${token.base_token_symbol}:`, message);

            // Mark the token as sent and prepare content
            console.log(`Content prepared and marked as sent for token: ${token.base_token_symbol} on chain: ${token.chain_id}`);
            preparedContent.push({ token, message, chartImage, kaboomUrl });
        }
    }
    return preparedContent;
}

// Helper function to shuffle an array (Fisher-Yates algorithm)
function shuffleArray(array) {
    for (let i = array.length - 1; i > 0; i--) {
        const j = Math.floor(Math.random() * (i + 1));
        [array[i], array[j]] = [array[j], array[i]]; // Swap elements
    }
    return array;
}

async function sendDexNotificationsToUsers(users, preparedContent) {
    // Shuffle the users array to randomize the order
    const shuffledUsers = shuffleArray(users);
    const BATCH_SIZE = 20;

    // Group users by language preference to avoid redundant translations
    const usersByLanguage = groupBy(shuffledUsers, (user) => user.language || 'en');

    // Process each language group
    for (const [lang, userGroup] of Object.entries(usersByLanguage)) {
        console.log(`Processing language group: ${lang}`);
        
        const languageContent = preparedContent.map(content => ({
            ...content,
            message: content.message
        }));

        // Process each batch of users in the language group
        for (let i = 0; i < userGroup.length; i += BATCH_SIZE) {
            const userBatch = userGroup.slice(i, i + BATCH_SIZE);

            try {
                await Promise.all(userBatch.map(async (user) => {
                    try {
                        // Fetch chat type to determine if it’s a group or individual
                        let chat;
                        try {
                            chat = await bot.telegram.getChat(user.telegram_id);
                        } catch (getChatError) {
                            return;
                        }

                        if (chat.type !== 'group' && chat.type !== 'supergroup') {
                            return;
                        }

                        const subscribedChains = Array.isArray(user.cex_subscriptions)
                            ? user.cex_subscriptions.map(sub => sub.toLowerCase())
                            : [];

                        // Filter content for chains user is subscribed to
                        const userSpecificContent = languageContent.filter(content =>
                            subscribedChains.includes(content.token.chain_id.toLowerCase())
                        );

                        if (userSpecificContent.length === 0) return;

                        // Send each content item to the user
                        for (const content of userSpecificContent) {
                            try {
                                await bot.telegram.sendPhoto(user.telegram_id, { source: content.chartImage }, {
                                    caption: content.message,
                                    parse_mode: 'HTML',
                                    ...Markup.inlineKeyboard([Markup.button.url(`🚀 ${t(lang, 'open_kaboom_bot_button')}`, content.kaboomUrl)])
                                });
                            } catch (sendError) {
                                console.error(`Failed to send message to user ${user.telegram_id} for token ${content.token.base_token_symbol}:`, sendError);
                            }
                        }
                    } catch (userError) {
                        console.error(`Error processing notifications for user ${user.telegram_id}:`, userError);
                    }
                }));

                console.log(`Completed sending notifications for batch ${Math.ceil(i / BATCH_SIZE) + 1} for language: ${lang}`);

            } catch (batchError) {
                console.error(`Error in batch ${Math.ceil(i / BATCH_SIZE) + 1} for language: ${lang}:`, batchError);
            }
        }
    }
}


// Helper functions

function formatSingleTokenMessage(token, lang = 'en') {
    const riskColor = getRiskEmoji(token.security_risk, lang);
    const riskText = `${t(lang, 'smart_contract_risk')} `;
    const KaBoomAlphaLink = '<a href="https://t.me/kaboom_signal"> @KaBoomAlpha</a>';

    return `
<b>$${token.base_token_symbol.toUpperCase()} ${t(lang, 'on')} ${capitalizeFirstLetter(token.chain_id)} ${t(lang, 'up')} ${formatPercent(token.price_change_h1, false)} ${t(lang, 'in')} ${t(lang, '1H')}! 🔥</b>
${t(lang, 'signal_on')} ${KaBoomAlphaLink}

📈 ${t(lang, '5m')}: ${formatPercent(token.price_change_m5, false)} | ${t(lang, '1h')}: ${formatPercent(token.price_change_h1, false)} | ${t(lang, '24h')}: ${formatPercent(token.price_change_h24, false)}

💰 ${t(lang, 'fdv')}: ${formatNumber(token.market_cap_kusd)} (${t(lang, 'thousand_usd')})
💦 ${t(lang, 'liquidity')}: ${formatNumber(token.liquidity_usd_kusd)} (${t(lang, 'thousand_usd')})
📊 ${t(lang, 'volume24h')}: ${formatNumber(token.volume_h24_kusd)} (${t(lang, 'thousand_usd')})

🔥 ${t(lang, 'burn')}: ${formatPercent(token.lp_locked_percent, true)} |${t(lang, 'top10')}: ${formatPercent(token.top_10_percent, true)} 
${riskColor} ${riskText} | ⏳ ${formatNumber(token.launched_days)}${t(lang, 'days')}

${generateLinks(token)}
`;
}

function groupBy(array, getKey) {
    return array.reduce((result, currentValue) => {
        const key = getKey(currentValue);
        (result[key] = result[key] || []).push(currentValue);
        return result;
    }, {});
}

// Function to get risk emoji with translated risk level
function getRiskEmoji(riskLevel, lang) {
    switch (riskLevel.toLowerCase()) {
        case 'low': return `🟢 ${t(lang, 'low')}`;
        case 'medium': return `🟡 ${t(lang, 'medium')}`;
        case 'high': return `🔴 ${t(lang, 'high')}`;
        default: return `⚪ ${t(lang, 'unknown')}`; // Default for unknown risk levels
    }
}

function capitalizeFirstLetter(string) {
    return string ? string.charAt(0).toUpperCase() + string.slice(1).toLowerCase() : 'N/A';
}

function formatPercent(value, multiply) {
    let numericValue = parseFloat(value); // Use `let` instead of `const`
    if (isNaN(numericValue) || numericValue === 0) return 'N/A';
    if (multiply) {
        numericValue *= 100; // This reassignment is now valid with `let`
    }
    return `${Math.round(numericValue)}%`;
}

function formatNumber(value) {
    const numericValue = parseFloat(value);
    return !isNaN(numericValue) ? Math.round(numericValue).toLocaleString() : 'N/A';
}

function generateLinks(token) {
    const website = token.website ? `<a href="${token.website}">Web</a>` : 'Web';
    const twitter = token.twitter ? `<a href="${token.twitter}">X</a>` : 'X';
    const telegram = token.telegram ? `<a href="${token.telegram}">TG</a>` : 'TG';
    const dexScreenerUrl = token.dexscreener ? `<a href="${token.dexscreener}">Screener</a>` : 'Screener';

    return ` ${website}   |   ${twitter}   |   ${telegram}   |   ${dexScreenerUrl}`;
}

async function getFirstTokenEntry(baseTokenAddress) {
    const firstEntryRes = await dbClient.query(`
        SELECT price_usd, insert_time 
        FROM token_data 
        WHERE base_token_address = $1 
        ORDER BY insert_time ASC 
        LIMIT 1;
    `, [baseTokenAddress]);

    // Query to get the total row count for this token
    const countRes = await dbClient.query(`
        SELECT COUNT(*) AS row_count 
        FROM token_data 
        WHERE base_token_address = $1;
    `, [baseTokenAddress]);

    const firstEntry = firstEntryRes.rows[0];
    const isFirstCall = countRes.rows[0].row_count === '1';

    return { firstEntry, isFirstCall };
}

const calculateTimeRange = (launchedDays) => {
    const currentTime = Math.floor(Date.now() / 1000);
    const maxDays = 30;
    const daysAgo = Math.min(launchedDays, maxDays);
    const timeFrom = currentTime - Math.floor(daysAgo * 24 * 60 * 60);
    return { timeFrom, timeTo: currentTime };
};

// Helper function to round down to the nearest hour
const roundDownToNearestHour = (timestamp) => {
    const date = new Date(timestamp * 1000);
    date.setMinutes(0, 0, 0); // Set minutes, seconds, and milliseconds to 0 to round down to the nearest hour
    return Math.floor(date.getTime() / 1000); // Return as UNIX timestamp
};

// Helper function to find the price in priceData for a given timestamp
const findPriceAtTimestamp = (priceData, timestamp) => {
    return priceData.find(point => point.unixTime === timestamp)?.value || null;
};

// Adjust buy signal times to match the nearest price in priceData
const adjustBuySignalTimes = (buySignals, priceData) => {
    return buySignals.map(signal => {
        const adjustedTime = roundDownToNearestHour(signal.time);
        const adjustedPrice = findPriceAtTimestamp(priceData, adjustedTime);
        return adjustedPrice !== null
            ? { time: adjustedTime, price: adjustedPrice }
            : null; // Return null if no matching price is found
    }).filter(signal => signal !== null); // Filter out any unmatched signals
};

const fetchPriceHistory = async (chainId, baseTokenAddress, launchedDays) => {
    const { timeFrom, timeTo } = calculateTimeRange(launchedDays);
    const dataType = launchedDays < 1 ? '5m' : '1H';

    const url = `https://public-api.birdeye.so/defi/history_price?address=${baseTokenAddress}&address_type=token&type=${dataType}&time_from=${timeFrom}&time_to=${timeTo}`;

    const options = {
        method: 'GET',
        headers: {
            accept: 'application/json',
            'x-chain': chainId,
            'X-API-KEY': process.env.KABOOM_ALPHA_BIRDEYE
        }
    };

    try {
        const response = await fetch(url, options);
        const data = await response.json();
        return data && data.data && Array.isArray(data.data.items) ? data.data.items : [];
    } catch (err) {
        console.error('Error fetching price history:', err);
        return [];
    }
};

// Function to fetch all historical buy signals for a token
const getTokenNotifications = async (baseTokenAddress) => {
    const notificationsRes = await dbClient.query(`
        SELECT insert_time, price_usd 
        FROM token_data 
        WHERE base_token_address = $1 
        ORDER BY insert_time ASC;
    `, [baseTokenAddress]);

    // Map results to an array of { time, price } for charting
    return notificationsRes.rows.map(row => ({
        time: new Date(row.insert_time).getTime() / 1000, // Convert to UNIX timestamp
        price: row.price_usd
    }));
};

const createPriceChart = async (priceData, tokenSymbol, tokenName, buySignals) => {
    const chartJSNodeCanvas = new ChartJSNodeCanvas({ width: 800, height: 600 });

    // Convert price data into labels and prices for the main price line
    const labels = priceData.map((point) => 
        new Date(point.unixTime * 1000).toISOString().slice(0, 19).replace('T', ' ')
    );
    const prices = priceData.map((point) => point.value);

    // Convert buy signals into points aligned with the price line
    const buySignalPoints = buySignals.map(signal => {
        // Find the price in `priceData` that matches the buy signal timestamp
        const matchingPricePoint = priceData.find(point => point.unixTime === signal.time);
        return {
            x: new Date(signal.time * 1000).toISOString().slice(0, 19).replace('T', ' '),
            y: matchingPricePoint ? matchingPricePoint.value : null // Use the green line's price if found
        };
    }).filter(point => point.y !== null); // Filter out any points without a matching price

    console.log('Buy signal points:', buySignalPoints);

    const configuration = {
        type: 'line',
        data: {
            labels,
            datasets: [
                {
                    label: `${tokenSymbol} | ${tokenName}`,
                    data: prices,
                    borderColor: 'rgba(0, 255, 0, 1)',
                    fill: false,
                    tension: 0.3,
                    pointRadius: 0, // Make the main line points invisible
                    pointHoverRadius: 0, // Make the main line points invisible on hover
                    borderWidth: 2, // Ensure the line is visible
                    order: 1 // Draw line first (behind markers)
                },
                {
                    label: 'Buy Signal',
                    data: buySignalPoints,
                    backgroundColor: 'yellow',
                    pointStyle: 'triangle', // Upward triangle
                    pointRadius: 6, // Visible point size for buy signals
                    borderColor: 'yellow',
                    borderWidth: 1,
                    showLine: false,
                    type: 'scatter', // Scatter type to ensure only markers are displayed
                    order: 2 // Draw markers on top of the line
                }
            ]
        },
        options: {
            plugins: {
                legend: {
                    labels: {
                        color: 'white',
                        font: { size: 16 * 1.3, family: 'DejaVu Sans' }
                    }
                }
            },
            scales: {
                x: {
                    display: true,
                    title: {
                        display: true,
                        text: 'Date',
                        color: 'rgba(255, 255, 255, 0.7)',
                        font: { size: 12 * 1.3, family: 'DejaVu Sans' }
                    },
                    ticks: {
                        color: 'rgba(255, 255, 255, 0.5)',
                        font: { size: 10 * 1.3, family: 'DejaVu Sans' },
                        autoSkip: true,
                        maxTicksLimit: 10
                    },
                    grid: { color: 'rgba(255, 255, 255, 0.1)' }
                },
                y: {
                    display: true,
                    title: {
                        display: true,
                        text: 'Price (USD)',
                        color: 'white',
                        font: { size: 12 * 1.3, family: 'DejaVu Sans' }
                    },
                    ticks: {
                        color: 'white',
                        font: { size: 10 * 1.3, family: 'DejaVu Sans' }
                    },
                    grid: { color: 'rgba(255, 255, 255, 0.1)' }
                }
            },
            layout: { padding: { top: 20, right: 30, bottom: 20, left: 30 } }
        },
        plugins: [{
            beforeDraw: (chart) => {
                const ctx = chart.canvas.getContext('2d');
                ctx.save();
                ctx.fillStyle = 'black';
                ctx.fillRect(0, 0, chart.width, chart.height);
                ctx.restore();
            }
        }]
    };

    return await chartJSNodeCanvas.renderToBuffer(configuration);
};

module.exports = {
    sendDexNotifications,
    formatSingleTokenMessage, 
    formatPercent, 
    formatNumber, 
    generateLinks,
    groupBy  
};
