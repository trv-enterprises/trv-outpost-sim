-- Initialize the sensors database schema
-- This script runs automatically when PostgreSQL container starts

-- Create sensors table
CREATE TABLE IF NOT EXISTS sensors (
    sensor_id VARCHAR(50) PRIMARY KEY,
    sensor_type VARCHAR(50) NOT NULL,
    unit VARCHAR(20) NOT NULL,
    location VARCHAR(100) NOT NULL,
    min_value DECIMAL(10,2),
    max_value DECIMAL(10,2),
    installed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    is_active BOOLEAN DEFAULT true
);

-- Create readings table
CREATE TABLE IF NOT EXISTS sensor_readings (
    id BIGSERIAL,
    timestamp TIMESTAMP NOT NULL,
    sensor_id VARCHAR(50) NOT NULL,
    value DECIMAL(12,4) NOT NULL,
    quality INTEGER DEFAULT 100,
    status VARCHAR(20) DEFAULT 'normal',
    PRIMARY KEY (id, timestamp)
);

-- Create indexes for efficient querying
CREATE INDEX IF NOT EXISTS idx_readings_timestamp ON sensor_readings(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_readings_sensor_id ON sensor_readings(sensor_id);
CREATE INDEX IF NOT EXISTS idx_readings_sensor_time ON sensor_readings(sensor_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_readings_status ON sensor_readings(status);

-- Create aggregated stats view
CREATE OR REPLACE VIEW sensor_stats AS
SELECT
    sensor_id,
    DATE_TRUNC('hour', timestamp) as hour,
    COUNT(*) as reading_count,
    AVG(value) as avg_value,
    MIN(value) as min_value,
    MAX(value) as max_value,
    AVG(quality) as avg_quality
FROM sensor_readings
GROUP BY sensor_id, DATE_TRUNC('hour', timestamp);

-- Create latest readings view
CREATE OR REPLACE VIEW latest_readings AS
SELECT DISTINCT ON (sensor_id)
    r.id,
    r.timestamp,
    r.sensor_id,
    s.sensor_type,
    r.value,
    s.unit,
    s.location,
    r.quality,
    r.status
FROM sensor_readings r
JOIN sensors s ON r.sensor_id = s.sensor_id
ORDER BY sensor_id, timestamp DESC;

-- Sample queries for dashboard use:
--
-- Get latest reading for all sensors:
-- SELECT * FROM latest_readings;
--
-- Get readings for a specific sensor in the last hour:
-- SELECT * FROM sensor_readings
-- WHERE sensor_id = 'sensor-001'
-- AND timestamp > NOW() - INTERVAL '1 hour'
-- ORDER BY timestamp DESC;
--
-- Get hourly averages for a sensor:
-- SELECT * FROM sensor_stats
-- WHERE sensor_id = 'sensor-001'
-- ORDER BY hour DESC
-- LIMIT 24;
--
-- Get all temperature sensors with warning status:
-- SELECT r.*, s.sensor_type
-- FROM sensor_readings r
-- JOIN sensors s ON r.sensor_id = s.sensor_id
-- WHERE s.sensor_type = 'temperature' AND r.status = 'warning';
