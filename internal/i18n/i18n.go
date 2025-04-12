package i18n

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"go.uber.org/zap"
	"golang.org/x/text/language"
)

//go:embed all:locales
var localeFS embed.FS

// Manager 管理 i18n Bundle
type Manager struct {
	bundle          *i18n.Bundle
	defaultLanguage language.Tag
	Logger          *zap.Logger
	localizers      map[string]*i18n.Localizer // Cache localizers
	availableLangs  map[string]string          // Map code (e.g., "en") to display name (e.g., "English")
}

// NewManager 创建一个新的 i18n 管理器
// langDir: 包含语言 JSON 文件的目录路径
// defaultLang: 默认语言代码 (例如 "en")
func NewManager(defaultLang string, logger *zap.Logger) (*Manager, error) {
	defaultLanguageTag, err := language.Parse(defaultLang)
	if err != nil {
		logger.Error("Failed to parse default language tag", zap.String("tag", defaultLang), zap.Error(err))
		return nil, fmt.Errorf("invalid default language tag '%s': %w", defaultLang, err)
	}

	bundle := i18n.NewBundle(defaultLanguageTag)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	m := &Manager{
		bundle:          bundle,
		defaultLanguage: defaultLanguageTag,
		Logger:          logger.Named("i18n"),
		localizers:      make(map[string]*i18n.Localizer),
		availableLangs:  make(map[string]string),
	}

	err = m.LoadTranslations()
	if err != nil {
		return nil, err
	}

	// Initialize localizers for available languages
	for langCode := range m.availableLangs {
		m.localizers[langCode] = i18n.NewLocalizer(m.bundle, langCode)
	}
	// Ensure default localizer exists
	if _, ok := m.localizers[defaultLang]; !ok {
		m.localizers[defaultLang] = i18n.NewLocalizer(m.bundle, defaultLang)
		// Add default lang to available if somehow missed during load
		if _, exists := m.availableLangs[defaultLang]; !exists {
			name := defaultLanguageTag.String()
			base, _ := defaultLanguageTag.Base()
			name = base.String() // Use base language name
			m.availableLangs[defaultLang] = name
			m.Logger.Warn("Default language was not found in locale files, added manually.", zap.String("lang", defaultLang))
		}
	}

	m.Logger.Info("i18n Manager initialized",
		zap.String("default_language", defaultLang),
		zap.Int("loaded_languages", len(m.availableLangs)),
	)
	return m, nil
}

func (m *Manager) LoadTranslations() error {
	// Register the TOML unmarshal function
	m.bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	m.Logger.Info("Loading translations from embedded FS", zap.String("dir", "."))
	files, err := fs.ReadDir(localeFS, "locales") // Read the root which corresponds to the embedded 'locales' dir
	if err != nil {
		m.Logger.Error("Failed to read embedded locales root directory", zap.Error(err))
		return fmt.Errorf("failed to read embedded locales directory: %w", err)
	}

	if len(files) == 0 {
		m.Logger.Warn("No locale files found in embedded locales directory")
		return errors.New("no locale files found")
	}

	loadedCount := 0
	m.availableLangs = make(map[string]string) // Initialize map
	for _, file := range files {
		fileName := file.Name()
		fmt.Println("fileName", fileName)
		// Expecting filenames like active.en.toml, active.zh.toml
		if !file.IsDir() && filepath.Ext(fileName) == ".toml" {
			filePathInFS := fileName
			m.Logger.Debug("Attempting to load translation file", zap.String("file", filePathInFS))
			// Load the message file using the registered unmarshaler
			_, err := m.bundle.LoadMessageFileFS(localeFS, "locales/"+filePathInFS)
			if err != nil {
				m.Logger.Warn("Failed to load translation file from embedded FS", zap.String("file", filePathInFS), zap.Error(err))
				continue // Skip this file
			}
			m.Logger.Info("Successfully loaded translation file", zap.String("file", filePathInFS))
			loadedCount++

			// Extract language code and name from filename like "active.en.toml"
			baseName := strings.TrimSuffix(fileName, ".toml") // e.g., "active.en" or "en"
			parts := strings.Split(baseName, ".")             // e.g., ["active", "en"] or ["en"]
			var langCode string
			if len(parts) >= 1 { // Allow both "en.toml" and "active.en.toml"
				langCode = parts[len(parts)-1] // Always take the last part as the code (e.g., "en")
			} else {
				m.Logger.Warn("Unexpected filename format, cannot extract language code", zap.String("file", fileName))
				continue
			}

			tag, parseErr := language.Parse(langCode)
			langDisplayName := langCode // Fallback to code
			if parseErr == nil {
				base, _ := tag.Base()
				langDisplayName = base.String() // e.g., "en"
				// TODO: Consider reading a display name from the TOML file itself if available, e.g., [_.name]
			} else {
				m.Logger.Warn("Failed to parse language code from filename", zap.String("file", fileName), zap.String("extractedCode", langCode), zap.Error(parseErr))
			}
			m.availableLangs[langCode] = langDisplayName // Store "en" -> "en"
			m.Logger.Debug("Registered available language", zap.String("code", langCode), zap.String("name", langDisplayName))

		} else if !file.IsDir() {
			m.Logger.Debug("Skipping non-matching file in locales dir", zap.String("file", fileName))
		}
	}

	if loadedCount == 0 {
		m.Logger.Error("No *.toml translation files were loaded") // Update error message
		return errors.New("no valid translation files loaded")
	}

	m.Logger.Info("Finished loading translations", zap.Int("loaded_count", loadedCount), zap.Any("available_languages", m.availableLangs))
	return nil
}

// T translates a message identified by key, using optional template data and plural count.
// It uses the v2 API of go-i18n.
// args can contain:
// - An int: interpreted as PluralCount.
// - Key-value pairs (string, interface{}, string, interface{}, ...): interpreted as TemplateData.
func (m *Manager) T(lang *string, key string, args ...interface{}) string {
	langCode := m.defaultLanguage.String()
	if lang != nil && *lang != "" {
		langCode = *lang
	}

	localizer, ok := m.localizers[langCode]
	if !ok {
		m.Logger.Warn("No localizer found for language, using default", zap.String("requested_lang", langCode), zap.String("default_lang", m.defaultLanguage.String()))
		localizer = m.localizers[m.defaultLanguage.String()]
		if localizer == nil { // Should not happen if init is correct
			m.Logger.Error("Default localizer is nil! Returning key.")
			return key // Absolute fallback
		}
	}

	localizeConfig := &i18n.LocalizeConfig{
		MessageID: key,
	}

	// Parse args for TemplateData and PluralCount
	templateData := make(map[string]interface{})
	var pluralCount *int

	i := 0
	for i < len(args) {
		switch v := args[i].(type) {
		case int:
			if pluralCount == nil {
				count := v
				pluralCount = &count
				i++
			} else {
				m.Logger.Warn("Multiple int arguments provided to T, only the first is used as PluralCount", zap.String("key", key))
				i++ // Skip subsequent ints
			}
		case string:
			if i+1 < len(args) {
				templateData[v] = args[i+1]
				i += 2
			} else {
				m.Logger.Warn("Odd number of arguments for TemplateData, skipping last string key", zap.String("key", key), zap.String("lastKey", v))
				i++ // Skip the dangling key
			}
		case map[string]interface{}: // Allow passing a pre-built map
			if len(templateData) == 0 { // Only accept the first map found
				templateData = v
			} else {
				m.Logger.Warn("Multiple map[string]interface{} arguments provided to T, only the first is used", zap.String("key", key))
			}
			i++
		default:
			m.Logger.Warn("Unsupported argument type in T", zap.String("key", key), zap.Any("type", fmt.Sprintf("%T", args[i])))
			i++ // Skip unsupported arg
		}
	}

	if len(templateData) > 0 {
		localizeConfig.TemplateData = templateData
	}
	if pluralCount != nil {
		localizeConfig.PluralCount = pluralCount
	}

	localized, err := localizer.Localize(localizeConfig)
	if err != nil {
		if !errors.Is(err, &i18n.MessageNotFoundErr{}) {
			m.Logger.Error("Failed to localize message",
				zap.String("key", key),
				zap.String("lang", langCode),
				zap.Any("templateData", templateData),
				zap.Any("pluralCount", pluralCount),
				zap.Error(err),
			)
		}
		return key
	}

	return localized
}

// GetAvailableLanguages returns a map of language codes to their display names.
func (m *Manager) GetAvailableLanguages() map[string]string {
	// Return a copy to prevent external modification
	langs := make(map[string]string)
	for code, name := range m.availableLangs {
		langs[code] = name
	}
	return langs
}

// GetLanguageName returns the display name for a given language code.
func (m *Manager) GetLanguageName(code string) (string, bool) {
	name, ok := m.availableLangs[code]
	return name, ok
}

// Helper to get the default language tag
func (m *Manager) GetDefaultLanguageTag() language.Tag {
	return m.defaultLanguage
}
