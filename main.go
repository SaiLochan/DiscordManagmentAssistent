package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ============================================================================
// GLOBAL STATE & DATA STRUCTURES
// ============================================================================

// Admin username - only this user can execute admin commands
const ADMIN_USERNAME = "slr8090"

// Global environment variables
var (
	GeminiAPIKey    string
	DiscordBotToken string
)

// Thread-safe data structures for managing leads, projects, and conversations
var (
	leadsList    *LeadsList
	projectsList *ProjectsList
	memory       *ConversationMemory
)

// ============================================================================
// LEADS MANAGEMENT DATA STRUCTURE
// ============================================================================

// LeadsList manages authorized leads with thread-safe operations
type LeadsList struct {
	sync.Mutex
	leads map[string]bool // username -> is_lead (boolean)
}

// NewLeadsList creates a new thread-safe leads list
func NewLeadsList() *LeadsList {
	return &LeadsList{
		leads: make(map[string]bool),
	}
}

// AddLead adds a user to the leads list, returns false if already exists
func (ll *LeadsList) AddLead(username string) bool {
	ll.Lock()
	defer ll.Unlock()

	// DEBUG: Log lead addition attempt
	fmt.Printf("[DEBUG] Attempting to add lead: %s\n", username)

	if ll.leads[username] {
		fmt.Printf("[DEBUG] Lead %s already exists\n", username)
		return false
	}

	ll.leads[username] = true
	fmt.Printf("[DEBUG] Successfully added lead: %s\n", username)
	return true
}

// RemoveLead removes a user from the leads list, returns false if not found
func (ll *LeadsList) RemoveLead(username string) bool {
	ll.Lock()
	defer ll.Unlock()

	// DEBUG: Log lead removal attempt
	fmt.Printf("[DEBUG] Attempting to remove lead: %s\n", username)

	if !ll.leads[username] {
		fmt.Printf("[DEBUG] Lead %s not found\n", username)
		return false
	}

	delete(ll.leads, username)
	fmt.Printf("[DEBUG] Successfully removed lead: %s\n", username)
	return true
}

// IsLead checks if a user is a lead (thread-safe)
func (ll *LeadsList) IsLead(username string) bool {
	ll.Lock()
	defer ll.Unlock()

	return ll.leads[username]
}

// GetAllLeads returns a copy of all leads
func (ll *LeadsList) GetAllLeads() []string {
	ll.Lock()
	defer ll.Unlock()

	var leads []string
	for lead := range ll.leads {
		leads = append(leads, lead)
	}

	fmt.Printf("[DEBUG] Retrieved %d leads: %v\n", len(leads), leads)
	return leads
}

// ============================================================================
// PROJECTS DATA STRUCTURE
// ============================================================================

// Project represents a project with all its details
type Project struct {
	ID          string    // Unique project ID (PRJ001, PRJ002, etc)
	Name        string    // Project name
	Description string    // Project description
	Leads       []string  // Array of usernames who are project leads
	Members     []string  // Array of team member usernames
	Tasks       []Task    // Array of tasks in this project
	Status      string    // Project status: Planning, Active, OnHold, Completed, Archived
	CreatedBy   string    // Username of creator
	CreatedDate time.Time // Creation date
	UpdatedDate time.Time // Last update date
	StartDate   time.Time // Project start date
	EndDate     time.Time // Project end/deadline date
}

// Task represents a task within a project
type Task struct {
	ID          string    // Unique task ID (TSK001, TSK002, etc)
	Name        string    // Task name
	Description string    // Task description
	ProjectID   string    // Reference to parent project ID
	AssignedTo  string    // Username of assigned team member (empty if unassigned)
	Status      string    // Task status: Open, InProgress, OnHold, Blocked, Completed
	Priority    string    // Task priority: Low, Medium, High, Critical
	Progress    int       // Progress percentage (0-100)
	CreatedBy   string    // Username of task creator
	CreatedDate time.Time // Creation date
	UpdatedDate time.Time // Last update date
	Deadline    time.Time // Task deadline
}

// ProjectsList manages all projects with thread-safe operations
type ProjectsList struct {
	sync.Mutex
	projects   map[string]*Project // projectID -> Project
	nextProjID int                 // Counter for generating next project ID
}

// NewProjectsList creates a new thread-safe projects list
func NewProjectsList() *ProjectsList {
	return &ProjectsList{
		projects:   make(map[string]*Project),
		nextProjID: 1,
	}
}

// AddProject adds a new project to the list
func (pl *ProjectsList) AddProject(project *Project) {
	pl.Lock()
	defer pl.Unlock()

	fmt.Printf("[DEBUG] Adding project: ID=%s, Name=%s\n", project.ID, project.Name)
	pl.projects[project.ID] = project
	pl.nextProjID++
	fmt.Printf("[DEBUG] Project added successfully, next ID will be PRJ%03d\n", pl.nextProjID)
}

// GetProject retrieves a single project by ID
func (pl *ProjectsList) GetProject(id string) *Project {
	pl.Lock()
	defer pl.Unlock()

	fmt.Printf("[DEBUG] Retrieving project: %s\n", id)
	project := pl.projects[id]

	if project == nil {
		fmt.Printf("[DEBUG] Project %s not found\n", id)
	}
	return project
}

// GetAllProjects returns all projects
func (pl *ProjectsList) GetAllProjects() []*Project {
	pl.Lock()
	defer pl.Unlock()

	var projects []*Project
	for _, p := range pl.projects {
		projects = append(projects, p)
	}

	fmt.Printf("[DEBUG] Retrieved %d total projects\n", len(projects))
	return projects
}

// UpdateProject updates an existing project
func (pl *ProjectsList) UpdateProject(project *Project) {
	pl.Lock()
	defer pl.Unlock()

	fmt.Printf("[DEBUG] Updating project: %s\n", project.ID)
	project.UpdatedDate = time.Now()
	pl.projects[project.ID] = project
	fmt.Printf("[DEBUG] Project updated successfully\n")
}

// GenerateProjectID generates the next project ID (PRJ001, PRJ002, etc)
func (pl *ProjectsList) GenerateProjectID() string {
	pl.Lock()
	defer pl.Unlock()

	projectID := fmt.Sprintf("PRJ%03d", pl.nextProjID)
	fmt.Printf("[DEBUG] Generated new project ID: %s\n", projectID)
	return projectID
}

// ============================================================================
// CONVERSATION MEMORY FOR GEMINI AI
// ============================================================================

// ConversationMemory stores conversation history per Discord channel
// This allows Gemini AI to maintain context across multiple messages
type ConversationMemory struct {
	sync.Mutex
	history map[string][]map[string]string // channelID -> conversation history
}

// NewConversationMemory creates a new conversation memory
func NewConversationMemory() *ConversationMemory {
	return &ConversationMemory{
		history: make(map[string][]map[string]string),
	}
}

// AddMessage adds a message to the conversation history
func (cm *ConversationMemory) AddMessage(channelID, role, content string) {
	cm.Lock()
	defer cm.Unlock()

	// DEBUG: Log message addition
	fmt.Printf("[DEBUG] Adding message to channel %s - Role: %s, Content length: %d\n", channelID, role, len(content))

	if _, exists := cm.history[channelID]; !exists {
		cm.history[channelID] = []map[string]string{}
	}

	cm.history[channelID] = append(cm.history[channelID], map[string]string{
		"role": role,
		"text": content,
	})

	fmt.Printf("[DEBUG] Channel %s now has %d messages\n", channelID, len(cm.history[channelID]))
}

// GetHistory retrieves conversation history for a channel
func (cm *ConversationMemory) GetHistory(channelID string) []map[string]string {
	cm.Lock()
	defer cm.Unlock()

	fmt.Printf("[DEBUG] Retrieving history for channel: %s\n", channelID)

	if history, exists := cm.history[channelID]; exists {
		fmt.Printf("[DEBUG] Found %d messages in history\n", len(history))
		return history
	}

	fmt.Printf("[DEBUG] No history found for channel, creating new\n")
	return []map[string]string{}
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

// isAdmin checks if a user is the admin (slr8090)
func isAdmin(username string) bool {
	fmt.Printf("[DEBUG] Checking if %s is admin\n", username)
	isAdminUser := username == ADMIN_USERNAME
	fmt.Printf("[DEBUG] %s is admin: %v\n", username, isAdminUser)
	return isAdminUser
}

// isLead checks if a user is either admin or a lead
func isLead(username string) bool {
	fmt.Printf("[DEBUG] Checking if %s is lead or admin\n", username)

	if isAdmin(username) {
		fmt.Printf("[DEBUG] %s is admin (therefore also a lead)\n", username)
		return true
	}

	isLeadUser := leadsList.IsLead(username)
	fmt.Printf("[DEBUG] %s is lead: %v\n", username, isLeadUser)
	return isLeadUser
}

// getMemberByName finds a server member by display name or username
func getMemberByName(s *discordgo.Session, guildID, nameToFind string) *discordgo.Member {
	fmt.Printf("[DEBUG] Searching for member: %s in guild: %s\n", nameToFind, guildID)

	members, err := s.GuildMembers(guildID, "", 1000)
	if err != nil {
		fmt.Printf("[ERROR] Failed to fetch members: %v\n", err)
		return nil
	}

	fmt.Printf("[DEBUG] Fetched %d members from guild\n", len(members))

	for _, member := range members {
		// Skip bot accounts
		if member.User.Bot {
			continue
		}

		// Get display name (nickname or username)
		displayName := member.Nick
		if displayName == "" {
			displayName = member.User.Username
		}

		// Check for exact match (case-insensitive)
		if strings.EqualFold(displayName, nameToFind) || strings.EqualFold(member.User.Username, nameToFind) {
			fmt.Printf("[DEBUG] Found member: %s (Username: %s)\n", displayName, member.User.Username)
			return member
		}
	}

	fmt.Printf("[DEBUG] Member %s not found\n", nameToFind)
	return nil
}

// ============================================================================
// COMMAND HANDLERS - PHASE 1: CORE FEATURES
// ============================================================================

// handleListCommands displays all available commands based on user role
func handleListCommands(username string) string {
	fmt.Printf("[DEBUG] Listing commands for user: %s (isAdmin: %v, isLead: %v)\n", username, isAdmin(username), isLead(username))

	if isAdmin(username) {
		// Admin sees ALL commands
		fmt.Println("[DEBUG] Returning admin command list")
		fmt.Println(

			`📋 **ALL COMMANDS (ADMIN ACCESS)**

🔷 PHASE 1: CORE FEATURES & USER MANAGEMENT
  d++1 - List all commands (this message)
  d++Names - Show all server member names
  d++Help {command} - Get detailed help for a command
  d++LNames - List all authorized leads
  d++LAdd {name} - Add a member as lead (ADMIN ONLY)
  d++LRem {name} - Remove a member from leads (ADMIN ONLY)
  d++WhoIsLead - Show online leads
  d++AI {message} - Send message to Gemini AI (ADMIN ONLY)
  d++exit - Shutdown bot (ADMIN ONLY)

🔷 PHASE 2: PROJECT MANAGEMENT CORE
  d++PList - List all projects
  d++PInfo {project_id} - View project details
  d++PCreate {project_name} - Create a new project (LEADS ONLY)
  d++PDesc {project_id} {description} - Set project description
  d++PStatus {project_id} {status} - Change project status
  d++PAddMember {project_id} {member_name} - Add member to project
  d++PRemMember {project_id} {member_name} - Remove member from project
  d++PSetLead {project_id} {member_name} - Set project lead

🔷 PHASE 3: TASK MANAGEMENT & ASSIGNMENT
  d++MyTasks - View your assigned tasks
  d++TInfo {task_id} - View detailed task information
  d++TCreate {project_id} {task_name} - Create a new task (LEADS ONLY)
  d++TList {project_id} - List all tasks in a project
  d++TAssign {task_id} {member_name} - Assign task to member
  d++TUnassign {task_id} - Unassign task from member
  d++TSetPriority {task_id} {priority} - Set task priority (Low/Medium/High/Critical)
  d++TSetDeadline {task_id} {date} - Set task deadline (YYYY-MM-DD)
  d++TSetStatus {task_id} {status} - Update task status

🔷 PHASE 4: TIMELINE, DEADLINES & SCHEDULING
  d++MyDeadlines - View your upcoming deadlines
  d++MyOverdue - Show your overdue tasks
  d++DueToday - Show tasks due today
  d++DeadlineOverview - Show company-wide deadline overview

🔷 PHASE 5: REPORTING, ANALYTICS & OPERATIONS
  d++CompanyStatus - View company dashboard (LEADS ONLY)
  d++ProjectHealth {project_id} - View project health status
  d++TeamHealth - View team operational health`,
		)
		return `📋 **ALL COMMANDS (ADMIN ACCESS)**

🔷 PHASE 1: CORE FEATURES & USER MANAGEMENT
  d++1 - List all commands (this message)
  d++Names - Show all server member names
  d++Help {command} - Get detailed help for a command
  d++LNames - List all authorized leads
  d++LAdd {name} - Add a member as lead (ADMIN ONLY)
  d++LRem {name} - Remove a member from leads (ADMIN ONLY)
  d++WhoIsLead - Show online leads
  d++AI {message} - Send message to Gemini AI (ADMIN ONLY)
  d++exit - Shutdown bot (ADMIN ONLY)

🔷 PHASE 2: PROJECT MANAGEMENT CORE
  d++PList - List all projects
  d++PInfo {project_id} - View project details
  d++PCreate {project_name} - Create a new project (LEADS ONLY)
  d++PDesc {project_id} {description} - Set project description
  d++PStatus {project_id} {status} - Change project status
  d++PAddMember {project_id} {member_name} - Add member to project
  d++PRemMember {project_id} {member_name} - Remove member from project
  d++PSetLead {project_id} {member_name} - Set project lead

🔷 PHASE 3: TASK MANAGEMENT & ASSIGNMENT
  d++MyTasks - View your assigned tasks
  d++TInfo {task_id} - View detailed task information
  d++TCreate {project_id} {task_name} - Create a new task (LEADS ONLY)
  d++TList {project_id} - List all tasks in a project
  d++TAssign {task_id} {member_name} - Assign task to member
  d++TUnassign {task_id} - Unassign task from member
  d++TSetPriority {task_id} {priority} - Set task priority (Low/Medium/High/Critical)
  d++TSetDeadline {task_id} {date} - Set task deadline (YYYY-MM-DD)
  d++TSetStatus {task_id} {status} - Update task status

🔷 PHASE 4: TIMELINE, DEADLINES & SCHEDULING
  d++MyDeadlines - View your upcoming deadlines
  d++MyOverdue - Show your overdue tasks
  d++DueToday - Show tasks due today
  d++DeadlineOverview - Show company-wide deadline overview

🔷 PHASE 5: REPORTING, ANALYTICS & OPERATIONS
  d++CompanyStatus - View company dashboard (LEADS ONLY)
  d++ProjectHealth {project_id} - View project health status
  d++TeamHealth - View team operational health`
	}

	if isLead(username) {
		// Leads see most commands except admin-specific ones
		fmt.Println("[DEBUG] Returning lead command list")
		return `📋 **AVAILABLE COMMANDS (LEAD ACCESS)**

🔷 PHASE 1: CORE FEATURES
  d++1 - List all commands
  d++Names - List all server members
  d++Help {command} - Get command help
  d++LNames - List authorized leads
  d++WhoIsLead - Show online leads
  d++AI {message} - Ask Gemini AI

🔷 PHASE 2: PROJECT MANAGEMENT
  d++PList - List all projects
  d++PInfo {project_id} - Project details
  d++PCreate {project_name} - Create project
  d++PAddMember {project_id} {name} - Add member
  d++PRemMember {project_id} {name} - Remove member
  d++PSetLead {project_id} {name} - Set project lead

🔷 PHASE 3: TASK MANAGEMENT
  d++MyTasks - Your tasks
  d++TCreate {project_id} {name} - Create task
  d++TList {project_id} - List project tasks
  d++TAssign {task_id} {name} - Assign task
  d++TSetStatus {task_id} {status} - Update task status

🔷 PHASE 4: DEADLINES
  d++MyDeadlines - Your deadlines
  d++MyOverdue - Your overdue tasks

🔷 PHASE 5: ANALYTICS
  d++CompanyStatus - Company dashboard`
	}

	// Team members see basic commands only
	fmt.Println("[DEBUG] Returning team member command list")
	return `📋 **AVAILABLE COMMANDS (TEAM MEMBER ACCESS)**

🔷 BASIC COMMANDS
  d++1 - List all commands
  d++Names - List server members
  d++Help {command} - Get help
  d++AI {message} - Ask Gemini AI

🔷 PERSONAL TASKS
  d++MyTasks - Your assigned tasks
  d++MyDeadlines - Your upcoming deadlines
  d++MyOverdue - Your overdue tasks`
}

// handleListNames displays all server members
func handleListNames(s *discordgo.Session, guildID string) string {
	fmt.Printf("[DEBUG] Getting all member names for guild: %s\n", guildID)

	members, err := s.GuildMembers(guildID, "", 1000)
	if err != nil {
		fmt.Printf("[ERROR] Failed to get guild members: %v\n", err)
		return "❌ Error fetching server members"
	}

	fmt.Printf("[DEBUG] Fetched %d raw members\n", len(members))

	var names []string
	for _, member := range members {
		// Skip bot accounts
		if member.User.Bot {
			fmt.Printf("[DEBUG] Skipping bot: %s\n", member.User.Username)
			continue
		}

		// Use nickname if available, otherwise username
		displayName := member.Nick
		if displayName == "" {
			displayName = member.User.Username
		}

		fmt.Printf("[DEBUG] Adding member: %s\n", displayName)
		names = append(names, displayName)
	}

	fmt.Printf("[DEBUG] Total members (non-bot): %d\n", len(names))

	if len(names) == 0 {
		return "❌ No members found in server"
	}

	response := fmt.Sprintf("👥 **Server Members (%d total):**\n\n", len(names))
	for i, name := range names {
		response += fmt.Sprintf("%d. %s\n", i+1, name)
	}

	return response
}

// handleHelp provides help for specific commands
func handleHelp(command string) string {
	command = strings.ToLower(command)
	command = strings.TrimPrefix(command, "d++")

	fmt.Printf("[DEBUG] Providing help for command: %s\n", command)

	helpText := map[string]string{
		"1":             "📖 Lists all available commands based on your access level (Team Member, Lead, or Admin)",
		"names":         "📖 Shows all server member names (including nicknames if set)",
		"help":          "📖 Shows detailed help for a specific command. Usage: d++Help {command}",
		"lnames":        "📖 Lists all users who have been authorized as leads",
		"ladd":          "📖 Adds a member as a lead (ADMIN ONLY). Usage: d++LAdd {member_name}",
		"lrem":          "📖 Removes a member from leads (ADMIN ONLY). Usage: d++LRem {member_name}",
		"whoislead":     "📖 Shows which leads are currently online",
		"ai":            "📖 Send a message to Gemini AI (ADMIN ONLY). It maintains conversation memory. Usage: d++AI {your_message}",
		"exit":          "📖 Shuts down the bot (ADMIN ONLY). This will stop all bot operations.",
		"plist":         "📖 Lists all active projects with their status and member count",
		"pinfo":         "📖 Shows detailed information about a specific project. Usage: d++PInfo {project_id}",
		"pcreate":       "📖 Creates a new project (LEADS ONLY). Usage: d++PCreate {project_name}",
		"padd":          "📖 Adds a member to a project. Usage: d++PAddMember {project_id} {member_name}",
		"mytasks":       "📖 Shows all tasks assigned to you",
		"tcreate":       "📖 Creates a new task in a project (LEADS ONLY). Usage: d++TCreate {project_id} {task_name}",
		"tlist":         "📖 Lists all tasks in a project. Usage: d++TList {project_id}",
		"mydeadlines":   "📖 Shows your upcoming deadlines sorted by urgency",
		"myoverdue":     "📖 Shows all your overdue tasks",
		"companystatus": "📖 Shows company-wide dashboard with project and task statistics (LEADS ONLY)",
	}

	if text, exists := helpText[command]; exists {
		fmt.Printf("[DEBUG] Help found for command: %s\n", command)
		return fmt.Sprintf("**Help: %s**\n\n%s", command, text)
	}

	fmt.Printf("[DEBUG] Help not found for command: %s\n", command)
	return fmt.Sprintf("❌ No help found for command: **%s**\nUse d++1 to see all available commands", command)
}

// handleListLeads displays all authorized leads
func handleListLeads() string {
	fmt.Println("[DEBUG] Executing handleListLeads")

	leads := leadsList.GetAllLeads()

	fmt.Printf("[DEBUG] Found %d leads\n", len(leads))

	if len(leads) == 0 {
		return "📝 **Authorized Leads:** None currently assigned"
	}

	response := fmt.Sprintf("👨‍💼 **Authorized Leads (%d total):**\n\n", len(leads))
	for i, lead := range leads {
		response += fmt.Sprintf("%d. %s ✓\n", i+1, lead)
	}

	return response
}

// handleAddLead adds a member as a lead (ADMIN ONLY)
func handleAddLead(s *discordgo.Session, guildID, nameToAdd string) string {
	fmt.Printf("[DEBUG] Adding lead: %s\n", nameToAdd)

	// Find the member in the guild
	member := getMemberByName(s, guildID, nameToAdd)
	if member == nil {
		fmt.Printf("[DEBUG] Member %s not found\n", nameToAdd)
		return fmt.Sprintf("❌ Member **%s** not found in server", nameToAdd)
	}

	fmt.Printf("[DEBUG] Found member, username: %s\n", member.User.Username)

	// Try to add the lead
	if leadsList.AddLead(member.User.Username) {
		fmt.Printf("[DEBUG] Successfully added %s as lead\n", member.User.Username)
		return fmt.Sprintf("✅ **%s** has been added as an authorized lead!", nameToAdd)
	}

	// Lead already exists
	fmt.Printf("[DEBUG] %s already is a lead\n", member.User.Username)
	return fmt.Sprintf("⚠️ **%s** is already an authorized lead", nameToAdd)
}

// handleRemoveLead removes a member from leads (ADMIN ONLY)
func handleRemoveLead(nameToRemove string) string {
	fmt.Printf("[DEBUG] Removing lead: %s\n", nameToRemove)

	if leadsList.RemoveLead(nameToRemove) {
		fmt.Printf("[DEBUG] Successfully removed %s from leads\n", nameToRemove)
		return fmt.Sprintf("✅ **%s** has been removed from leads", nameToRemove)
	}

	fmt.Printf("[DEBUG] %s is not in the leads list\n", nameToRemove)
	return fmt.Sprintf("❌ **%s** is not currently an authorized lead", nameToRemove)
}

// handleWhoIsLead shows online leads
func handleWhoIsLead() string {
	fmt.Println("[DEBUG] Executing handleWhoIsLead")

	leads := leadsList.GetAllLeads()

	if len(leads) == 0 {
		return "📋 **Authorized Leads:** None assigned"
	}

	response := fmt.Sprintf("👥 **Online Leads Status (%d total):**\n\n🟢 **ONLINE:**\n", len(leads))
	for _, lead := range leads {
		response += fmt.Sprintf("  • %s\n", lead)
	}

	return response
}

// ============================================================================
// COMMAND HANDLERS - PHASE 2: PROJECT MANAGEMENT
// ============================================================================

// handlePList lists all projects
func handlePList(username string) string {
	fmt.Printf("[DEBUG] Listing projects for user: %s (isLead: %v)\n", username, isLead(username))

	projects := projectsList.GetAllProjects()

	fmt.Printf("[DEBUG] Found %d total projects\n", len(projects))

	if len(projects) == 0 {
		return "📋 **Projects:** No projects created yet. Leads can create projects using d++PCreate"
	}

	response := fmt.Sprintf("📊 **Active Projects (%d total):**\n\n", len(projects))

	for i, p := range projects {
		fmt.Printf("[DEBUG] Adding project to list: %s - %s\n", p.ID, p.Name)

		if isLead(username) {
			// Leads see full information
			response += fmt.Sprintf("%d. [**%s**] **%s**\n", i+1, p.ID, p.Name)
			response += fmt.Sprintf("   📝 Description: %s\n", p.Description)
			response += fmt.Sprintf("   👤 Leads: %s\n", strings.Join(p.Leads, ", "))
			response += fmt.Sprintf("   👥 Members: %d\n", len(p.Members))
			response += fmt.Sprintf("   📋 Tasks: %d\n", len(p.Tasks))
			response += fmt.Sprintf("   🎯 Status: **%s** | 📅 Created: %s\n\n", p.Status, p.CreatedDate.Format("2006-01-02"))
		} else {
			// Team members see basic information
			response += fmt.Sprintf("%d. **%s** [%s] - %d members\n", i+1, p.Name, p.Status, len(p.Members))
		}
	}

	return response
}

// handlePInfo shows detailed project information
func handlePInfo(projectID, username string) string {
	fmt.Printf("[DEBUG] Getting project info for: %s (user: %s, isLead: %v)\n", projectID, username, isLead(username))

	project := projectsList.GetProject(projectID)
	if project == nil {
		fmt.Printf("[DEBUG] Project %s not found\n", projectID)
		return fmt.Sprintf("❌ Project **%s** not found", projectID)
	}

	fmt.Printf("[DEBUG] Found project: %s\n", project.Name)

	if isLead(username) {
		// Leads see full details
		fmt.Println("[DEBUG] Returning full project info for lead")

		daysUntilDeadline := int(time.Until(project.EndDate).Hours() / 24)
		deadlineIndicator := "🟢"
		if daysUntilDeadline < 0 {
			deadlineIndicator = "🔴"
		} else if daysUntilDeadline <= 7 {
			deadlineIndicator = "🟠"
		}

		return fmt.Sprintf(`📋 **PROJECT DETAILS: %s [%s]**

📝 **Description:** %s

👤 **Project Leads:** %s
👥 **Team Members (%d):** %s

📊 **Tasks:** %d total

🎯 **Status:** %s
📅 **Created:** %s by %s
⏳ **Deadline:** %s %s (%d days)
📅 **Start Date:** %s
`, project.Name, project.ID,
			project.Description,
			strings.Join(project.Leads, ", "),
			len(project.Members), strings.Join(project.Members, ", "),
			len(project.Tasks),
			project.Status,
			project.CreatedDate.Format("2006-01-02"), project.CreatedBy,
			project.EndDate.Format("2006-01-02"), deadlineIndicator, daysUntilDeadline,
			project.StartDate.Format("2006-01-02"))
	}

	// Team members see limited details
	fmt.Println("[DEBUG] Returning limited project info for team member")

	return fmt.Sprintf(`📋 **PROJECT: %s [%s]**

📝 **Description:** %s

👥 **Team Members (%d):** %s

🎯 **Status:** %s`,
		project.Name, project.ID,
		project.Description,
		len(project.Members), strings.Join(project.Members, ", "),
		project.Status)
}

// handlePCreate creates a new project (LEADS ONLY)
func handlePCreate(username, projectName string) string {
	fmt.Printf("[DEBUG] Creating project: '%s' by user: %s\n", projectName, username)

	// Generate new project ID
	projectID := projectsList.GenerateProjectID()

	// Create project
	project := &Project{
		ID:          projectID,
		Name:        projectName,
		Description: "No description set yet",
		Leads:       []string{username},
		Members:     []string{username},
		Tasks:       []Task{},
		Status:      "Planning",
		CreatedBy:   username,
		CreatedDate: time.Now(),
		UpdatedDate: time.Now(),
		StartDate:   time.Now(),
		EndDate:     time.Now().AddDate(0, 1, 0), // Default 1 month from now
	}

	// Add to projects list
	projectsList.AddProject(project)

	fmt.Printf("[DEBUG] Project created successfully: %s\n", projectID)

	return fmt.Sprintf(`✅ **PROJECT CREATED SUCCESSFULLY!**

🆔 **Project ID:** %s
📋 **Project Name:** %s
👤 **Created by:** %s
📅 **Created:** %s
🎯 **Status:** Planning
⏳ **Default Deadline:** %s (1 month from now)

You are now the default lead for this project.

**Next Steps:**
• Use d++PDesc %s {description} to add a description
• Use d++PAddMember %s {member_name} to add team members
• Use d++TCreate %s {task_name} to create tasks`, projectID, projectName, username, time.Now().Format("2006-01-02"), time.Now().AddDate(0, 1, 0).Format("2006-01-02"), projectID, projectID, projectID)
}

// handlePAddMember adds a member to a project
func handlePAddMember(s *discordgo.Session, guildID, projectID, memberName, requesterName string) string {
	fmt.Printf("[DEBUG] Adding member '%s' to project '%s' by '%s'\n", memberName, projectID, requesterName)

	// Get project
	project := projectsList.GetProject(projectID)
	if project == nil {
		fmt.Printf("[DEBUG] Project %s not found\n", projectID)
		return fmt.Sprintf("❌ Project **%s** not found", projectID)
	}

	// Check if requester is project lead or admin
	isProjectLead := false
	for _, lead := range project.Leads {
		if lead == requesterName || isAdmin(requesterName) {
			isProjectLead = true
			break
		}
	}

	if !isProjectLead {
		fmt.Printf("[DEBUG] User %s is not a project lead\n", requesterName)
		return "❌ Only project leads can add members to their project"
	}

	fmt.Printf("[DEBUG] User is authorized to add members\n")

	// Find member in guild
	member := getMemberByName(s, guildID, memberName)
	if member == nil {
		fmt.Printf("[DEBUG] Member %s not found in guild\n", memberName)
		return fmt.Sprintf("❌ Member **%s** not found in server", memberName)
	}

	fmt.Printf("[DEBUG] Found member, checking if already in project\n")

	// Check if already a member
	for _, existingMember := range project.Members {
		if existingMember == member.User.Username {
			fmt.Printf("[DEBUG] %s is already a member\n", member.User.Username)
			return fmt.Sprintf("⚠️ **%s** is already a member of this project", memberName)
		}
	}

	// Add member to project
	project.Members = append(project.Members, member.User.Username)
	projectsList.UpdateProject(project)

	fmt.Printf("[DEBUG] Successfully added member to project\n")

	return fmt.Sprintf(`✅ **MEMBER ADDED TO PROJECT**

📋 **Project:** %s [%s]
👤 **Member Added:** %s (%s)
👥 **Total Members:** %d

You can now assign tasks to %s in this project.`, project.Name, projectID, memberName, member.User.Username, len(project.Members), memberName)
}

// handlePRemMember removes a member from a project
func handlePRemMember(projectID, memberName, requesterName string) string {
	fmt.Printf("[DEBUG] Removing member '%s' from project '%s' by '%s'\n", memberName, projectID, requesterName)

	project := projectsList.GetProject(projectID)
	if project == nil {
		fmt.Printf("[DEBUG] Project %s not found\n", projectID)
		return fmt.Sprintf("❌ Project **%s** not found", projectID)
	}

	// Check if requester is project lead
	isProjectLead := false
	for _, lead := range project.Leads {
		if lead == requesterName || isAdmin(requesterName) {
			isProjectLead = true
			break
		}
	}

	if !isProjectLead {
		fmt.Printf("[DEBUG] User is not authorized\n")
		return "❌ Only project leads can remove members"
	}

	fmt.Printf("[DEBUG] User is authorized\n")

	// Find and remove member
	for i, member := range project.Members {
		if strings.EqualFold(member, memberName) {
			project.Members = append(project.Members[:i], project.Members[i+1:]...)
			projectsList.UpdateProject(project)

			fmt.Printf("[DEBUG] Successfully removed member\n")

			return fmt.Sprintf(`✅ **MEMBER REMOVED FROM PROJECT**

📋 **Project:** %s [%s]
👤 **Member Removed:** %s
👥 **Remaining Members:** %d`, project.Name, projectID, memberName, len(project.Members))
		}
	}

	fmt.Printf("[DEBUG] Member not found in project\n")
	return fmt.Sprintf("❌ **%s** is not a member of this project", memberName)
}

// ============================================================================
// COMMAND HANDLERS - PHASE 3: TASK MANAGEMENT
// ============================================================================

// handleMyTasks shows tasks assigned to the user
func handleMyTasks(username string) string {
	fmt.Printf("[DEBUG] Getting tasks for user: %s\n", username)

	projects := projectsList.GetAllProjects()
	var userTasks []Task

	// Search for user's tasks across all projects
	for _, project := range projects {
		fmt.Printf("[DEBUG] Checking project %s for user's tasks\n", project.ID)

		for _, task := range project.Tasks {
			if task.AssignedTo == username {
				fmt.Printf("[DEBUG] Found task assigned to %s: %s\n", username, task.ID)
				userTasks = append(userTasks, task)
			}
		}
	}

	fmt.Printf("[DEBUG] Total tasks found for user: %d\n", len(userTasks))

	if len(userTasks) == 0 {
		return "ℹ️ You don't have any assigned tasks right now"
	}

	response := fmt.Sprintf("📋 **Your Assigned Tasks (%d total):**\n\n", len(userTasks))

	for i, task := range userTasks {
		daysUntilDeadline := int(time.Until(task.Deadline).Hours() / 24)
		urgency := "🟢"
		if daysUntilDeadline < 0 {
			urgency = "🔴"
		} else if daysUntilDeadline <= 1 {
			urgency = "🔴"
		} else if daysUntilDeadline <= 3 {
			urgency = "🟠"
		}

		response += fmt.Sprintf("%d. %s [%s] **%s**\n", i+1, urgency, task.ID, task.Name)
		response += fmt.Sprintf("   📊 Status: %s | Priority: %s | Progress: %d%%\n", task.Status, task.Priority, task.Progress)
		if !task.Deadline.IsZero() {
			response += fmt.Sprintf("   📅 Deadline: %s (%d days)\n", task.Deadline.Format("2006-01-02"), daysUntilDeadline)
		}
		response += "\n"
	}

	return response
}

// handleTCreate creates a new task (LEADS ONLY)
func handleTCreate(projectID, taskName, username string) string {
	fmt.Printf("[DEBUG] Creating task: '%s' in project: %s by user: %s\n", taskName, projectID, username)

	// Get project
	project := projectsList.GetProject(projectID)
	if project == nil {
		fmt.Printf("[DEBUG] Project %s not found\n", projectID)
		return fmt.Sprintf("❌ Project **%s** not found", projectID)
	}

	// Check if user is project lead
	isProjectLead := false
	for _, lead := range project.Leads {
		if lead == username || isAdmin(username) {
			isProjectLead = true
			break
		}
	}

	if !isProjectLead {
		fmt.Printf("[DEBUG] User is not a project lead\n")
		return "❌ Only project leads can create tasks"
	}

	fmt.Printf("[DEBUG] User is authorized to create task\n")

	// Generate task ID
	taskNum := len(project.Tasks) + 1
	taskID := fmt.Sprintf("TSK%03d", taskNum)

	// Create task
	task := Task{
		ID:          taskID,
		Name:        taskName,
		Description: "No description set yet",
		ProjectID:   projectID,
		Status:      "Open",
		Priority:    "Medium",
		Progress:    0,
		CreatedBy:   username,
		CreatedDate: time.Now(),
		UpdatedDate: time.Now(),
		Deadline:    time.Now().AddDate(0, 0, 7), // Default 7 days from now
	}

	// Add task to project
	project.Tasks = append(project.Tasks, task)
	projectsList.UpdateProject(project)

	fmt.Printf("[DEBUG] Task created successfully: %s\n", taskID)

	return fmt.Sprintf(`✅ **TASK CREATED SUCCESSFULLY!**

📋 **Task:** %s [%s]
🎯 **Project:** %s [%s]

📝 **Description:** %s
📊 **Status:** Open
🔴 **Priority:** Medium
⏳ **Progress:** 0%%
📅 **Deadline:** %s (7 days from now)
👤 **Created by:** %s

**Next Steps:**
• Use d++TAssign %s {member_name} to assign the task
• Use d++TSetPriority %s {priority} to change priority
• Use d++TSetDeadline %s {date} to set deadline`, taskName, taskID, project.Name, projectID, task.Description, time.Now().AddDate(0, 0, 7).Format("2006-01-02"), username, taskID, taskID, taskID)
}

// handleTList lists all tasks in a project
func handleTList(projectID, username string) string {
	fmt.Printf("[DEBUG] Listing tasks for project: %s (user: %s, isLead: %v)\n", projectID, username, isLead(username))

	project := projectsList.GetProject(projectID)
	if project == nil {
		fmt.Printf("[DEBUG] Project %s not found\n", projectID)
		return fmt.Sprintf("❌ Project **%s** not found", projectID)
	}

	fmt.Printf("[DEBUG] Found project, filtering tasks\n")

	var tasks []Task

	// Filter tasks based on user role
	if isLead(username) {
		// Leads see all tasks
		tasks = project.Tasks
		fmt.Printf("[DEBUG] Showing all %d tasks for lead\n", len(tasks))
	} else {
		// Team members only see their own tasks
		for _, task := range project.Tasks {
			if task.AssignedTo == username {
				tasks = append(tasks, task)
			}
		}
		fmt.Printf("[DEBUG] Showing %d tasks for team member\n", len(tasks))
	}

	if len(tasks) == 0 {
		if isLead(username) {
			return fmt.Sprintf("📋 No tasks in project **%s** yet", project.Name)
		}
		return fmt.Sprintf("📋 You have no tasks in project **%s**", project.Name)
	}

	response := fmt.Sprintf("📋 **TASKS: %s [%s] (%d total):**\n\n", project.Name, projectID, len(tasks))

	for i, task := range tasks {
		urgency := "🟢"
		daysUntilDeadline := 0
		if !task.Deadline.IsZero() {
			daysUntilDeadline = int(time.Until(task.Deadline).Hours() / 24)
			if daysUntilDeadline < 0 {
				urgency = "🔴"
			} else if daysUntilDeadline <= 1 {
				urgency = "🔴"
			} else if daysUntilDeadline <= 3 {
				urgency = "🟠"
			}
		}

		response += fmt.Sprintf("%d. %s [%s] **%s**\n", i+1, urgency, task.ID, task.Name)
		response += fmt.Sprintf("   📊 Status: %s | Priority: %s | Progress: %d%%\n", task.Status, task.Priority, task.Progress)
		response += fmt.Sprintf("   👤 Assigned: %s\n", task.AssignedTo)
		if !task.Deadline.IsZero() {
			response += fmt.Sprintf("   📅 Deadline: %s\n", task.Deadline.Format("2006-01-02"))
		}
		response += "\n"
	}

	return response
}

// handleTAssign assigns a task to a member
func handleTAssign(s *discordgo.Session, guildID, taskID, memberName, requesterName string) string {
	fmt.Printf("[DEBUG] Assigning task %s to member %s by %s\n", taskID, memberName, requesterName)

	// Find the task
	var task *Task
	var project *Project

	projects := projectsList.GetAllProjects()
	for _, p := range projects {
		for i, t := range p.Tasks {
			if t.ID == taskID {
				task = &p.Tasks[i]
				project = p
				break
			}
		}
		if task != nil {
			break
		}
	}

	if task == nil {
		fmt.Printf("[DEBUG] Task %s not found\n", taskID)
		return fmt.Sprintf("❌ Task **%s** not found", taskID)
	}

	fmt.Printf("[DEBUG] Found task, checking authorization\n")

	// Check if requester is project lead
	isProjectLead := false
	for _, lead := range project.Leads {
		if lead == requesterName || isAdmin(requesterName) {
			isProjectLead = true
			break
		}
	}

	if !isProjectLead {
		fmt.Printf("[DEBUG] User is not authorized\n")
		return "❌ Only project leads can assign tasks"
	}

	fmt.Printf("[DEBUG] User is authorized\n")

	// Find member
	member := getMemberByName(s, guildID, memberName)
	if member == nil {
		fmt.Printf("[DEBUG] Member %s not found\n", memberName)
		return fmt.Sprintf("❌ Member **%s** not found in server", memberName)
	}

	fmt.Printf("[DEBUG] Found member, updating task assignment\n")

	// Assign task
	task.AssignedTo = member.User.Username
	task.UpdatedDate = time.Now()
	projectsList.UpdateProject(project)

	fmt.Printf("[DEBUG] Task assigned successfully\n")

	return fmt.Sprintf(`✅ **TASK ASSIGNED**

📋 **Task:** %s [%s]
📝 **Name:** %s
👤 **Assigned to:** %s (%s)
📊 **Status:** %s
🔴 **Priority:** %s`, task.Name, taskID, task.Name, memberName, member.User.Username, task.Status, task.Priority)
}

// handleTSetPriority sets task priority
func handleTSetPriority(taskID, priority, requesterName string) string {
	fmt.Printf("[DEBUG] Setting priority for task %s to %s\n", taskID, priority)

	// Validate priority
	validPriorities := map[string]bool{"Low": true, "Medium": true, "High": true, "Critical": true}
	if !validPriorities[priority] {
		fmt.Printf("[DEBUG] Invalid priority: %s\n", priority)
		return "❌ Priority must be: Low, Medium, High, or Critical"
	}

	// Find task
	var task *Task
	var project *Project

	projects := projectsList.GetAllProjects()
	for _, p := range projects {
		for i, t := range p.Tasks {
			if t.ID == taskID {
				task = &p.Tasks[i]
				project = p
				break
			}
		}
		if task != nil {
			break
		}
	}

	if task == nil {
		fmt.Printf("[DEBUG] Task %s not found\n", taskID)
		return fmt.Sprintf("❌ Task **%s** not found", taskID)
	}

	// Check authorization
	isProjectLead := false
	for _, lead := range project.Leads {
		if lead == requesterName || isAdmin(requesterName) {
			isProjectLead = true
			break
		}
	}

	if !isProjectLead {
		fmt.Printf("[DEBUG] User not authorized\n")
		return "❌ Only project leads can change task priority"
	}

	// Update priority
	task.Priority = priority
	task.UpdatedDate = time.Now()
	projectsList.UpdateProject(project)

	fmt.Printf("[DEBUG] Priority updated successfully\n")

	return fmt.Sprintf(`✅ **PRIORITY UPDATED**

📋 **Task:** %s [%s]
🔴 **New Priority:** %s`, task.Name, taskID, priority)
}

// handleTSetDeadline sets task deadline
func handleTSetDeadline(taskID, dateStr, requesterName string) string {
	fmt.Printf("[DEBUG] Setting deadline for task %s to %s\n", taskID, dateStr)

	// Parse date (format: YYYY-MM-DD)
	deadline, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		fmt.Printf("[DEBUG] Invalid date format: %s\n", dateStr)
		return "❌ Invalid date format. Use: YYYY-MM-DD (e.g., 2025-12-25)"
	}

	fmt.Printf("[DEBUG] Parsed deadline: %v\n", deadline)

	// Find task
	var task *Task
	var project *Project

	projects := projectsList.GetAllProjects()
	for _, p := range projects {
		for i, t := range p.Tasks {
			if t.ID == taskID {
				task = &p.Tasks[i]
				project = p
				break
			}
		}
		if task != nil {
			break
		}
	}

	if task == nil {
		fmt.Printf("[DEBUG] Task %s not found\n", taskID)
		return fmt.Sprintf("❌ Task **%s** not found", taskID)
	}

	// Check authorization
	isProjectLead := false
	for _, lead := range project.Leads {
		if lead == requesterName || isAdmin(requesterName) {
			isProjectLead = true
			break
		}
	}

	if !isProjectLead {
		fmt.Printf("[DEBUG] User not authorized\n")
		return "❌ Only project leads can change deadlines"
	}

	// Update deadline
	task.Deadline = deadline
	task.UpdatedDate = time.Now()
	projectsList.UpdateProject(project)

	fmt.Printf("[DEBUG] Deadline updated successfully\n")

	daysUntilDeadline := int(time.Until(deadline).Hours() / 24)

	return fmt.Sprintf(`✅ **DEADLINE UPDATED**

📋 **Task:** %s [%s]
📅 **New Deadline:** %s
⏳ **Days Until Deadline:** %d`, task.Name, taskID, deadline.Format("2006-01-02"), daysUntilDeadline)
}

// handleTSetStatus updates task status
func handleTSetStatus(taskID, status, requesterName string) string {
	fmt.Printf("[DEBUG] Setting status for task %s to %s\n", taskID, status)

	// Validate status
	validStatuses := map[string]bool{"Open": true, "InProgress": true, "OnHold": true, "Blocked": true, "Completed": true}
	if !validStatuses[status] {
		fmt.Printf("[DEBUG] Invalid status: %s\n", status)
		return "❌ Status must be: Open, InProgress, OnHold, Blocked, or Completed"
	}

	// Find task
	var task *Task
	var project *Project

	projects := projectsList.GetAllProjects()
	for _, p := range projects {
		for i, t := range p.Tasks {
			if t.ID == taskID {
				task = &p.Tasks[i]
				project = p
				break
			}
		}
		if task != nil {
			break
		}
	}

	if task == nil {
		fmt.Printf("[DEBUG] Task %s not found\n", taskID)
		return fmt.Sprintf("❌ Task **%s** not found", taskID)
	}

	// Check authorization (assigned user or project lead can change status)
	authorized := false
	if task.AssignedTo == requesterName {
		authorized = true
	}
	for _, lead := range project.Leads {
		if lead == requesterName || isAdmin(requesterName) {
			authorized = true
			break
		}
	}

	if !authorized {
		fmt.Printf("[DEBUG] User not authorized\n")
		return "❌ Only task assignee or project leads can change status"
	}

	// Update status
	oldStatus := task.Status
	task.Status = status
	task.UpdatedDate = time.Now()
	projectsList.UpdateProject(project)

	fmt.Printf("[DEBUG] Status updated from %s to %s\n", oldStatus, status)

	emoji := ""
	if status == "Completed" {
		emoji = "✅"
	} else if status == "InProgress" {
		emoji = "⚙️"
	} else if status == "OnHold" {
		emoji = "⏸️"
	} else if status == "Blocked" {
		emoji = "🚫"
	} else {
		emoji = "🆕"
	}

	return fmt.Sprintf(`%s **STATUS UPDATED**

📋 **Task:** %s [%s]
📊 **Status Changed:** %s → %s`, emoji, task.Name, taskID, oldStatus, status)
}

// ============================================================================
// COMMAND HANDLERS - PHASE 4: DEADLINES & TIMELINES
// ============================================================================

// handleMyDeadlines shows upcoming deadlines
func handleMyDeadlines(username string) string {
	fmt.Printf("[DEBUG] Getting deadlines for user: %s\n", username)

	projects := projectsList.GetAllProjects()
	var deadlines []Task

	// Find all tasks with deadlines assigned to user
	for _, project := range projects {
		for _, task := range project.Tasks {
			if task.AssignedTo == username && !task.Deadline.IsZero() {
				deadlines = append(deadlines, task)
				fmt.Printf("[DEBUG] Found deadline: %s - %s\n", task.ID, task.Name)
			}
		}
	}

	fmt.Printf("[DEBUG] Total deadlines found: %d\n", len(deadlines))

	if len(deadlines) == 0 {
		return "ℹ️ You have no upcoming deadlines"
	}

	// Sort by deadline (soonest first)
	for i := 0; i < len(deadlines); i++ {
		for j := i + 1; j < len(deadlines); j++ {
			if deadlines[j].Deadline.Before(deadlines[i].Deadline) {
				deadlines[i], deadlines[j] = deadlines[j], deadlines[i]
			}
		}
	}

	response := fmt.Sprintf("📅 **YOUR DEADLINES (%d total):**\n\n", len(deadlines))

	for i, task := range deadlines {
		daysUntil := int(time.Until(task.Deadline).Hours() / 24)
		urgency := "🟢"
		if daysUntil < 0 {
			urgency = "🔴 OVERDUE"
		} else if daysUntil == 0 {
			urgency = "🔴 TODAY!"
		} else if daysUntil == 1 {
			urgency = "🟠 TOMORROW"
		} else if daysUntil <= 7 {
			urgency = "🟠"
		}

		response += fmt.Sprintf("%d. %s [%s] **%s**\n", i+1, urgency, task.ID, task.Name)
		response += fmt.Sprintf("   📅 Due: %s | Priority: %s\n", task.Deadline.Format("2006-01-02"), task.Priority)
		response += "\n"
	}

	return response
}

// handleMyOverdue shows overdue tasks
func handleMyOverdue(username string) string {
	fmt.Printf("[DEBUG] Getting overdue tasks for user: %s\n", username)

	projects := projectsList.GetAllProjects()
	var overdueTasks []Task

	// Find all overdue tasks
	now := time.Now()
	for _, project := range projects {
		for _, task := range project.Tasks {
			if task.AssignedTo == username && !task.Deadline.IsZero() && task.Deadline.Before(now) && task.Status != "Completed" {
				overdueTasks = append(overdueTasks, task)
				fmt.Printf("[DEBUG] Found overdue task: %s\n", task.ID)
			}
		}
	}

	fmt.Printf("[DEBUG] Total overdue tasks: %d\n", len(overdueTasks))

	if len(overdueTasks) == 0 {
		return "✅ You have no overdue tasks!"
	}

	response := fmt.Sprintf("🔴 **OVERDUE TASKS (%d total):**\n\n", len(overdueTasks))

	for i, task := range overdueTasks {
		daysOverdue := int(time.Since(task.Deadline).Hours() / 24)

		response += fmt.Sprintf("%d. [%s] **%s**\n", i+1, task.ID, task.Name)
		response += fmt.Sprintf("   ⏰ Overdue by: %d days\n", daysOverdue)
		response += fmt.Sprintf("   Was due: %s\n", task.Deadline.Format("2006-01-02"))
		response += fmt.Sprintf("   Priority: %s\n\n", task.Priority)
	}

	return response
}

// ============================================================================
// COMMAND HANDLERS - PHASE 5: REPORTING & ANALYTICS
// ============================================================================

// handleCompanyStatus shows company dashboard (LEADS ONLY)
func handleCompanyStatus() string {
	fmt.Println("[DEBUG] Generating company status dashboard")

	projects := projectsList.GetAllProjects()

	totalProjects := len(projects)
	activeProjects := 0
	totalMembers := 0
	totalTasks := 0
	completedTasks := 0
	overdueTasks := 0

	for _, p := range projects {
		if p.Status == "Active" {
			activeProjects++
		}
		totalMembers += len(p.Members)
		totalTasks += len(p.Tasks)

		now := time.Now()
		for _, t := range p.Tasks {
			if t.Status == "Completed" {
				completedTasks++
			}
			if !t.Deadline.IsZero() && t.Deadline.Before(now) && t.Status != "Completed" {
				overdueTasks++
			}
		}
	}

	completionRate := 0
	if totalTasks > 0 {
		completionRate = (completedTasks * 100) / totalTasks
	}

	leads := leadsList.GetAllLeads()

	fmt.Printf("[DEBUG] Dashboard stats - Projects: %d, Tasks: %d, Completion: %d%%\n", totalProjects, totalTasks, completionRate)

	return fmt.Sprintf(`📊 **COMPANY STATUS DASHBOARD**

🎯 **PROJECTS:**
  Total: %d | Active: %d | Completed: %d
  Status: 🟢 OPERATIONAL

📋 **TASKS:**
  Total: %d | Completed: %d | Completion Rate: %d%%
  ⚠️ Overdue: %d

👥 **TEAM:**
  Total Members: %d
  Authorized Leads: %d

📈 **SYSTEM STATUS:**
  🟢 Bot Online and Operating
  🟢 Database Synchronized
  🟢 AI Integration Active

🕐 **Last Updated:** %s`, totalProjects, activeProjects, (totalProjects - activeProjects), totalTasks, completedTasks, completionRate, overdueTasks, totalMembers, len(leads), time.Now().Format("2006-01-02 15:04:05 MST"))
}

// ============================================================================
// GEMINI AI INTEGRATION
// ============================================================================

// callGeminiAPIWithMemory sends message to Gemini API with conversation history
func callGeminiAPIWithMemory(channelID, userMessage string) (string, error) {
	fmt.Printf("[DEBUG] Calling Gemini API for channel: %s\n", channelID)

	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key=" + GeminiAPIKey

	// Get conversation history
	history := memory.GetHistory(channelID)
	fmt.Printf("[DEBUG] Retrieved %d previous messages from history\n", len(history))

	// Build request with conversation history
	var parts []map[string]interface{}

	// Add all previous messages to context
	for _, msg := range history {
		parts = append(parts, map[string]interface{}{
			"text": msg["text"],
		})
		fmt.Printf("[DEBUG] Adding previous message to context: %s\n", msg["role"])
	}

	// Add current user message
	parts = append(parts, map[string]interface{}{
		"text": userMessage,
	})
	fmt.Printf("[DEBUG] Added current user message to context\n")

	// Create request body
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": parts,
			},
		},
	}

	// Convert to JSON
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		fmt.Printf("[ERROR] JSON marshaling failed: %v\n", err)
		return "", err
	}

	fmt.Printf("[DEBUG] Sending request to Gemini API (payload size: %d bytes)\n", len(jsonData))

	// Make HTTP POST request
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("[ERROR] HTTP request failed: %v\n", err)
		return "", err
	}
	defer resp.Body.Close()

	fmt.Printf("[DEBUG] Received response status: %d\n", resp.StatusCode)

	// Read response
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("[ERROR] Failed to read response body: %v\n", err)
		return "", err
	}

	fmt.Printf("[DEBUG] Response size: %d bytes\n", len(body))

	// Parse JSON response
	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		fmt.Printf("[ERROR] JSON parsing failed: %v\n", err)
		fmt.Printf("[DEBUG] Response body: %s\n", string(body))
		return "", err
	}

	fmt.Println("[DEBUG] Successfully parsed JSON response")

	// Extract the text from the response
	candidates, ok := result["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		fmt.Println("[ERROR] No candidates in response")
		return "No response from Gemini", nil
	}

	fmt.Printf("[DEBUG] Found %d candidates\n", len(candidates))

	candidate := candidates[0].(map[string]interface{})
	content := candidate["content"].(map[string]interface{})
	contentParts, ok := content["parts"].([]interface{})
	if !ok {
		fmt.Println("[ERROR] Failed to extract parts from content")
		return "Error parsing response", nil
	}

	fmt.Printf("[DEBUG] Found %d parts in content\n", len(contentParts))

	part := contentParts[0].(map[string]interface{})
	text, ok := part["text"].(string)
	if !ok {
		fmt.Println("[ERROR] Failed to extract text from part")
		return "Error extracting text from response", nil
	}

	fmt.Printf("[DEBUG] Gemini AI response: %s\n", text[:min(len(text), 100)])

	return text, nil
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ============================================================================
// MAIN MESSAGE HANDLER
// ============================================================================

// messageHandler processes all incoming Discord messages
func messageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	fmt.Printf("[DEBUG] Message received from: %s (ID: %s)\n", m.Author.Username, m.Author.ID)
	fmt.Printf("[DEBUG] Message content: %s\n", m.Content)

	// Ignore bot's own messages
	if m.Author.ID == s.State.User.ID {
		fmt.Println("[DEBUG] Ignoring bot's own message")
		return
	}

	// Check for command prefix
	if !strings.HasPrefix(m.Content, "d++") {
		fmt.Println("[DEBUG] Message doesn't have d++ prefix, ignoring")
		return
	}

	fmt.Println("[DEBUG] Message has d++ prefix, processing as command")

	// Remove prefix and parse
	commandText := strings.TrimPrefix(m.Content, "d++")
	parts := strings.Fields(commandText)

	if len(parts) == 0 {
		fmt.Println("[DEBUG] No command found after prefix")
		return
	}

	command := parts[0]
	var params []string
	if len(parts) > 1 {
		params = parts[1:]
	}

	fmt.Printf("[DEBUG] Parsed command: '%s' with %d parameters\n", command, len(params))

	var response string

	// Route commands
	switch command {
	// PHASE 1: CORE FEATURES
	case "1":
		fmt.Println("[DEBUG] Executing command: List Commands")
		response = handleListCommands(m.Author.Username)

	case "Names":
		fmt.Println("[DEBUG] Executing command: List Names")
		response = handleListNames(s, m.GuildID)

	case "Help":
		fmt.Println("[DEBUG] Executing command: Help")
		if len(params) == 0 {
			response = "❌ Usage: d++Help {command}"
		} else {
			response = handleHelp(strings.Join(params, " "))
		}

	case "LNames":
		fmt.Println("[DEBUG] Executing command: List Leads")
		if !isLead(m.Author.Username) {
			fmt.Println("[DEBUG] User not authorized")
			response = "❌ Access Denied: Only leads can view leads"
		} else {
			response = handleListLeads()
		}

	case "LAdd":
		fmt.Println("[DEBUG] Executing command: Add Lead")
		if !isAdmin(m.Author.Username) {
			fmt.Println("[DEBUG] User is not admin")
			response = "❌ Access Denied: Only slr8090 can add leads"
		} else if len(params) == 0 {
			response = "❌ Usage: d++LAdd {member_name}"
		} else {
			response = handleAddLead(s, m.GuildID, strings.Join(params, " "))
		}

	case "LRem":
		fmt.Println("[DEBUG] Executing command: Remove Lead")
		if !isAdmin(m.Author.Username) {
			fmt.Println("[DEBUG] User is not admin")
			response = "❌ Access Denied: Only slr8090 can remove leads"
		} else if len(params) == 0 {
			response = "❌ Usage: d++LRem {member_name}"
		} else {
			response = handleRemoveLead(strings.Join(params, " "))
		}

	case "WhoIsLead":
		fmt.Println("[DEBUG] Executing command: Who Is Lead")
		if !isLead(m.Author.Username) {
			response = "❌ Access Denied"
		} else {
			response = handleWhoIsLead()
		}

	case "AI":
		fmt.Println("[DEBUG] Executing command: Ask AI")
		if !isAdmin(m.Author.Username) {
			fmt.Println("[DEBUG] User is not admin")
			response = "❌ Access Denied: Only slr8090 can use AI"
		} else if len(params) == 0 {
			response = "❌ Usage: d++AI {your_message}"
		} else {
			userMessage := strings.Join(params, " ")
			fmt.Printf("[DEBUG] Sending to Gemini: %s\n", userMessage)

			memory.AddMessage(m.ChannelID, "user", userMessage)

			aiResponse, err := callGeminiAPIWithMemory(m.ChannelID, userMessage)
			if err != nil {
				fmt.Printf("[ERROR] Gemini API error: %v\n", err)
				response = "❌ Error calling Gemini API"
			} else {
				memory.AddMessage(m.ChannelID, "model", aiResponse)
				response = aiResponse
			}
		}

	case "exit":
		fmt.Println("[DEBUG] Executing command: Exit Bot")
		if !isAdmin(m.Author.Username) {
			fmt.Println("[DEBUG] User is not admin")
			response = "❌ Access Denied: Only slr8090 can shut down"
		} else {
			fmt.Println("[DEBUG] Shutting down bot...")
			s.ChannelMessageSend(m.ChannelID, "👋 **BOT SHUTTING DOWN...** Goodbye!")
			s.Close()
			os.Exit(0)
		}

	// PHASE 2: PROJECT MANAGEMENT
	case "PList":
		fmt.Println("[DEBUG] Executing command: Project List")
		response = handlePList(m.Author.Username)

	case "PInfo":
		fmt.Println("[DEBUG] Executing command: Project Info")
		if len(params) == 0 {
			response = "❌ Usage: d++PInfo {project_id}"
		} else {
			response = handlePInfo(params[0], m.Author.Username)
		}

	case "PCreate":
		fmt.Println("[DEBUG] Executing command: Create Project")
		if !isLead(m.Author.Username) {
			fmt.Println("[DEBUG] User is not a lead")
			response = "❌ Access Denied: Only leads can create projects"
		} else if len(params) == 0 {
			response = "❌ Usage: d++PCreate {project_name}"
		} else {
			response = handlePCreate(m.Author.Username, strings.Join(params, " "))
		}

	case "PAddMember":
		fmt.Println("[DEBUG] Executing command: Add Member to Project")
		if !isLead(m.Author.Username) {
			fmt.Println("[DEBUG] User is not a lead")
			response = "❌ Access Denied"
		} else if len(params) < 2 {
			response = "❌ Usage: d++PAddMember {project_id} {member_name}"
		} else {
			projectID := params[0]
			memberName := strings.Join(params[1:], " ")
			response = handlePAddMember(s, m.GuildID, projectID, memberName, m.Author.Username)
		}

	case "PRemMember":
		fmt.Println("[DEBUG] Executing command: Remove Member from Project")
		if !isLead(m.Author.Username) {
			fmt.Println("[DEBUG] User is not a lead")
			response = "❌ Access Denied"
		} else if len(params) < 2 {
			response = "❌ Usage: d++PRemMember {project_id} {member_name}"
		} else {
			projectID := params[0]
			memberName := strings.Join(params[1:], " ")
			response = handlePRemMember(projectID, memberName, m.Author.Username)
		}

	// PHASE 3: TASK MANAGEMENT
	case "MyTasks":
		fmt.Println("[DEBUG] Executing command: My Tasks")
		response = handleMyTasks(m.Author.Username)

	case "TCreate":
		fmt.Println("[DEBUG] Executing command: Create Task")
		if !isLead(m.Author.Username) {
			fmt.Println("[DEBUG] User is not a lead")
			response = "❌ Access Denied: Only leads can create tasks"
		} else if len(params) < 2 {
			response = "❌ Usage: d++TCreate {project_id} {task_name}"
		} else {
			projectID := params[0]
			taskName := strings.Join(params[1:], " ")
			response = handleTCreate(projectID, taskName, m.Author.Username)
		}

	case "TList":
		fmt.Println("[DEBUG] Executing command: List Tasks")
		if len(params) == 0 {
			response = "❌ Usage: d++TList {project_id}"
		} else {
			response = handleTList(params[0], m.Author.Username)
		}

	case "TAssign":
		fmt.Println("[DEBUG] Executing command: Assign Task")
		if !isLead(m.Author.Username) {
			fmt.Println("[DEBUG] User is not a lead")
			response = "❌ Access Denied"
		} else if len(params) < 2 {
			response = "❌ Usage: d++TAssign {task_id} {member_name}"
		} else {
			taskID := params[0]
			memberName := strings.Join(params[1:], " ")
			response = handleTAssign(s, m.GuildID, taskID, memberName, m.Author.Username)
		}

	case "TSetPriority":
		fmt.Println("[DEBUG] Executing command: Set Task Priority")
		if !isLead(m.Author.Username) {
			fmt.Println("[DEBUG] User is not a lead")
			response = "❌ Access Denied"
		} else if len(params) < 2 {
			response = "❌ Usage: d++TSetPriority {task_id} {priority}"
		} else {
			taskID := params[0]
			priority := params[1]
			response = handleTSetPriority(taskID, priority, m.Author.Username)
		}

	case "TSetDeadline":
		fmt.Println("[DEBUG] Executing command: Set Task Deadline")
		if !isLead(m.Author.Username) {
			fmt.Println("[DEBUG] User is not a lead")
			response = "❌ Access Denied"
		} else if len(params) < 2 {
			response = "❌ Usage: d++TSetDeadline {task_id} {date_YYYY-MM-DD}"
		} else {
			taskID := params[0]
			date := params[1]
			response = handleTSetDeadline(taskID, date, m.Author.Username)
		}

	case "TSetStatus":
		fmt.Println("[DEBUG] Executing command: Set Task Status")
		if len(params) < 2 {
			response = "❌ Usage: d++TSetStatus {task_id} {status}"
		} else {
			taskID := params[0]
			status := params[1]
			response = handleTSetStatus(taskID, status, m.Author.Username)
		}

	// PHASE 4: DEADLINES
	case "MyDeadlines":
		fmt.Println("[DEBUG] Executing command: My Deadlines")
		response = handleMyDeadlines(m.Author.Username)

	case "MyOverdue":
		fmt.Println("[DEBUG] Executing command: My Overdue Tasks")
		response = handleMyOverdue(m.Author.Username)

	// PHASE 5: ANALYTICS
	case "CompanyStatus":
		fmt.Println("[DEBUG] Executing command: Company Status")
		if !isLead(m.Author.Username) {
			fmt.Println("[DEBUG] User is not a lead")
			response = "❌ Access Denied: Only leads can view company status"
		} else {
			response = handleCompanyStatus()
		}

	default:
		fmt.Printf("[DEBUG] Unknown command: %s\n", command)
		response = fmt.Sprintf("❌ Unknown command: **%s**\nUse d++1 to see all available commands", command)
	}

	// Send response
	if response != "" {
		fmt.Printf("[DEBUG] Sending response (length: %d bytes)\n", len(response))
		s.ChannelMessageSend(m.ChannelID, response)
		fmt.Println("[DEBUG] Response sent successfully")
	}
}

// ============================================================================
// MAIN FUNCTION
// ============================================================================

func main() {
	fmt.Println("=========================================================")
	fmt.Println("🤖 DISCORD BOT MANAGEMENT SYSTEM - INITIALIZATION")
	fmt.Println("=========================================================")

	// Initialize data structures
	fmt.Println("[INIT] Initializing data structures...")
	leadsList = NewLeadsList()
	projectsList = NewProjectsList()
	memory = NewConversationMemory()
	fmt.Println("[INIT] ✓ Data structures initialized")

	// Load environment variables
	fmt.Println("[INIT] Loading environment variables from .env file...")
	err := godotenv.Load()
	if err != nil {
		log.Fatal("❌ Error loading .env file")
	}

	envMap, err := godotenv.Read()
	if err != nil {
		log.Fatal("❌ Error reading .env file")
	}

	DiscordBotToken = envMap["DiscordBotToken"]
	GeminiAPIKey = envMap["GeminiAPIKey"]

	if DiscordBotToken == "" {
		log.Fatal("❌ DiscordBotToken not found in .env file")
	}
	if GeminiAPIKey == "" {
		log.Fatal("❌ GeminiAPIKey not found in .env file")
	}

	fmt.Println("[INIT] ✓ Environment variables loaded")
	fmt.Printf("[INIT] Bot Token: %s...%s\n", DiscordBotToken[:10], DiscordBotToken[len(DiscordBotToken)-5:])
	fmt.Printf("[INIT] Gemini API Key: %s...%s\n", GeminiAPIKey[:10], GeminiAPIKey[len(GeminiAPIKey)-5:])

	// Create Discord session
	fmt.Println("[INIT] Creating Discord session...")
	sess, err := discordgo.New(DiscordBotToken)
	if err != nil {
		log.Fatal("❌ Error creating Discord session:", err)
	}

	fmt.Println("[INIT] ✓ Discord session created")

	// Add message handler
	fmt.Println("[INIT] Registering message handler...")
	sess.AddHandler(messageHandler)
	fmt.Println("[INIT] ✓ Message handler registered")

	// Set intents
	fmt.Println("[INIT] Setting Discord gateway intents...")
	sess.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentMessageContent
	fmt.Println("[INIT] ✓ Intents configured")

	// Open connection
	fmt.Println("[INIT] Opening Discord connection...")
	err = sess.Open()
	if err != nil {
		log.Fatal("❌ Error opening Discord connection:", err)
	}

	fmt.Println("[INIT] ✓ Discord connection established")
	defer sess.Close()

	fmt.Println("=========================================================")
	fmt.Println("✅ 🤖 DISCORD BOT MANAGEMENT SYSTEM IS ONLINE!")
	fmt.Println("=========================================================")
	fmt.Println("[STATUS] Admin User: slr8090")
	fmt.Println("[STATUS] Command Prefix: d++")
	fmt.Println("[STATUS] Waiting for commands...")
	fmt.Println("=========================================================")

	// Wait for interrupt signal to shutdown
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	fmt.Println("\n=========================================================")
	fmt.Println("👋 Bot shutting down gracefully...")
	fmt.Println("=========================================================")
}
